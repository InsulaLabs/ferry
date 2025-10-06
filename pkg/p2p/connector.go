package p2p

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)

type Conn struct {
	logger       *slog.Logger
	dataChannel  *webrtc.DataChannel
	peerConn     *webrtc.PeerConnection
	readChan     chan []byte
	shutdownChan chan struct{}
	closeOnce    sync.Once
	isConnected  chan struct{}
	dcReady      chan struct{}

	pendingCandidates []*webrtc.ICECandidate
	candidatesMux     sync.Mutex
	iceDone           chan struct{}
}

type SignalingData struct {
	SDP        string                    `json:"sdp,omitempty"`
	Candidates []webrtc.ICECandidateInit `json:"candidates,omitempty"`
}

func newConn(logger *slog.Logger) (*Conn, error) {
	if logger == nil {
		logger = slog.Default()
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
					"stun:stun1.l.google.com:19302",
					"stun:stun2.l.google.com:19302",
					"stun:stun3.l.google.com:19302",
					"stun:stun4.l.google.com:19302",
				},
			},
		},
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	}

	peerConn, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	conn := &Conn{
		logger:       logger,
		peerConn:     peerConn,
		readChan:     make(chan []byte, 100),
		shutdownChan: make(chan struct{}),
		isConnected:  make(chan struct{}),
		dcReady:      make(chan struct{}),
		iceDone:      make(chan struct{}),
	}

	peerConn.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		logger.Info("New ICE candidate", "candidate", c)
		conn.candidatesMux.Lock()
		conn.pendingCandidates = append(conn.pendingCandidates, c)
		conn.candidatesMux.Unlock()
	})

	peerConn.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		logger.Info("WebRTC connection state changed", "state", s.String())
		if s == webrtc.PeerConnectionStateConnected {
			close(conn.isConnected)
		}
	})

	peerConn.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		logger.Info("ICE connection state changed", "state", s.String())
	})

	peerConn.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		if state == webrtc.ICEGathererStateComplete {
			close(conn.iceDone)
		}
	})

	return conn, nil
}

// Implement net.Conn interface
func (c *Conn) Read(b []byte) (n int, err error) {
	<-c.dcReady
	select {
	case data := <-c.readChan:
		return copy(b, data), nil
	case <-c.shutdownChan:
		return 0, io.EOF
	}
}

func (c *Conn) Write(b []byte) (n int, err error) {
	<-c.dcReady
	err = c.dataChannel.Send(b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.shutdownChan)
		if c.dataChannel != nil {
			c.dataChannel.Close()
		}
		err = c.peerConn.Close()
	})
	return err
}

func (c *Conn) LocalAddr() net.Addr  { return nil }
func (c *Conn) RemoteAddr() net.Addr { return nil }

func (c *Conn) SetDeadline(t time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(t time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }

func (c *Conn) setupDataChannel(d *webrtc.DataChannel) {
	c.dataChannel = d

	// TODO: Look into this
	d.SetBufferedAmountLowThreshold(1024)

	d.OnOpen(func() {
		c.logger.Info("Data channel opened", "label", d.Label())
		close(c.dcReady)
	})

	d.OnClose(func() {
		c.logger.Info("Data channel closed", "label", d.Label())
	})

	d.OnError(func(err error) {
		c.logger.Error("Data channel error", "label", d.Label(), "error", err)
	})

	d.OnMessage(func(msg webrtc.DataChannelMessage) {
		c.readChan <- msg.Data
	})
}

func newOffer(logger *slog.Logger) (*Conn, *SignalingData, error) {
	conn, err := newConn(logger)
	if err != nil {
		return nil, nil, err
	}

	dataChannel, err := conn.peerConn.CreateDataChannel("data", nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	conn.setupDataChannel(dataChannel)

	offer, err := conn.peerConn.CreateOffer(nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	err = conn.peerConn.SetLocalDescription(offer)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	select {
	case <-conn.iceDone:
	case <-time.After(2 * time.Second): // fallback timeout
		conn.logger.Info("ICE gathering timed out, proceeding with current candidates")
	}

	conn.candidatesMux.Lock()
	sigData := &SignalingData{
		SDP:        offer.SDP,
		Candidates: make([]webrtc.ICECandidateInit, len(conn.pendingCandidates)),
	}
	for i, c := range conn.pendingCandidates {
		sigData.Candidates[i] = c.ToJSON()
	}
	conn.candidatesMux.Unlock()

	return conn, sigData, nil
}

func newAnswer(logger *slog.Logger, offerData *SignalingData) (*Conn, *SignalingData, error) {
	conn, err := newConn(logger)
	if err != nil {
		return nil, nil, err
	}

	conn.peerConn.OnDataChannel(func(d *webrtc.DataChannel) {
		conn.setupDataChannel(d)
	})

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerData.SDP,
	}

	err = conn.peerConn.SetRemoteDescription(offer)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	for _, candidate := range offerData.Candidates {
		err = conn.peerConn.AddICECandidate(candidate)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
	}

	answer, err := conn.peerConn.CreateAnswer(nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	err = conn.peerConn.SetLocalDescription(answer)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	select {
	case <-conn.iceDone:
	case <-time.After(2 * time.Second): // fallback timeout
		conn.logger.Info("ICE gathering timed out, proceeding with current candidates")
	}

	conn.candidatesMux.Lock()
	sigData := &SignalingData{
		SDP:        answer.SDP,
		Candidates: make([]webrtc.ICECandidateInit, len(conn.pendingCandidates)),
	}
	for i, c := range conn.pendingCandidates {
		sigData.Candidates[i] = c.ToJSON()
	}
	conn.candidatesMux.Unlock()

	return conn, sigData, nil
}

func (c *Conn) AcceptAnswer(answerData *SignalingData) error {
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerData.SDP,
	}

	err := c.peerConn.SetRemoteDescription(answer)
	if err != nil {
		return err
	}

	for _, candidate := range answerData.Candidates {
		err = c.peerConn.AddICECandidate(candidate)
		if err != nil {
			return err
		}
	}

	c.logger.Info("Waiting for WebRTC connection to be established...")
	<-c.isConnected
	c.logger.Info("WebRTC connection established, waiting for data channel...")
	<-c.dcReady
	c.logger.Info("Data channel ready")
	return nil
}

func Offer(logger *slog.Logger) (*Conn, *SignalingData, error) {
	return newOffer(logger)
}

func Answer(logger *slog.Logger, offerData *SignalingData) (*Conn, *SignalingData, error) {
	return newAnswer(logger, offerData)
}
