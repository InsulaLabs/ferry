package p2p

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

type Server struct {
	signalingURL string
	offerID      string
	listener     *Listener
	ctx          context.Context
	cancel       context.CancelFunc
	token        string

	listenBuffer int
	logger       *slog.Logger
}

// Listener implements net.Listener for WebRTC
type Listener struct {
	server      *Server
	connChan    chan *Conn
	ctx         context.Context
	cancel      context.CancelFunc
	currentConn *Conn
	mu          sync.Mutex
}

type ServerConfig struct {
	Logger       *slog.Logger
	Context      context.Context
	SignalingURL string
	OfferID      string
	Token        string
	ListenBuffer int
}

/*
	------------------------------------------------------------------------------------

	This is what could be considered the "client" side of the p2p connection.
	Its the part that says "hey I want to talk to you"

	------------------------------------------------------------------------------------
*/

type DialConfig struct {
	Logger       *slog.Logger
	Token        string
	SignalingURL string
	OfferID      string
	SkipVerify   bool
}

func Dial(config DialConfig) (net.Conn, error) {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	signaling := newSignalClient(signalClientOptions{
		token:      config.Token,
		url:        config.SignalingURL,
		skipVerify: config.SkipVerify,
		logger:     config.Logger.With("dialer", "Dial"),
	})

	offerData, err := signaling.getOffer(config.OfferID)
	if err != nil {
		config.Logger.Error("error getting offer", "error", err)
		return nil, err
	}

	// Create answer
	conn, answerData, err := newAnswer(config.Logger, offerData)
	if err != nil {
		config.Logger.Error("error creating answer", "error", err)
		return nil, err
	}

	// Send answer
	if err := signaling.sendAnswer(config.OfferID, answerData); err != nil {
		config.Logger.Error("error sending answer", "error", err)
		conn.Close()
		return nil, err
	}

	return conn, nil
}

/*
	------------------------------------------------------------------------------------

	This is what could be considered the "server" side of the p2p connection.
	It's the part that when "Listen()" is called on, you get a "listener" that binds
	to the teknometry signaling server and waits for offers "offer to talk".

	"Listen()" is effectively plugging in a phone and telling the phone company
	what your phone number is.

	Then someone who the company trusts (or has an existing connection with) calls
	and they happen to know the number we told them we will answer at, they can call
	us.

	This is why the higher-level library "teknet" has "Call()" and "Callable()" to indicate
	that we have "become reachable" and "might accept calls"

	------------------------------------------------------------------------------------
*/

func NewServer(config ServerConfig) (*Server, error) {
	ctx, cancel := context.WithCancel(config.Context)

	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	if config.ListenBuffer <= 0 {
		config.ListenBuffer = 10
	}

	s := &Server{
		signalingURL: config.SignalingURL,
		offerID:      config.OfferID,
		ctx:          ctx,
		cancel:       cancel,
		token:        config.Token,
		listenBuffer: config.ListenBuffer,
		logger:       config.Logger,
	}

	return s, nil
}

// Listen creates a net.Listener that accepts WebRTC connections
func (s *Server) Listen() (*Listener, error) {
	ctx, cancel := context.WithCancel(s.ctx)
	l := &Listener{
		server:   s,
		connChan: make(chan *Conn, s.listenBuffer),
		ctx:      ctx,
		cancel:   cancel,
	}

	// Start the offer loop
	go l.offerLoop()

	return l, nil
}

func (l *Listener) offerLoop() {
	signaling := newSignalClient(signalClientOptions{
		url:        l.server.signalingURL,
		skipVerify: true,
		token:      l.server.token,
		logger:     l.server.logger.With("listener", "offerLoop"),
	})

	for {
		select {
		case <-l.ctx.Done():
			return
		default:
			conn, sigData, err := newOffer(l.server.logger.With("listener", "offerLoop"))
			if err != nil {
				continue
			}

			l.mu.Lock()
			l.currentConn = conn
			l.mu.Unlock()

			// Register offer
			if err := signaling.registerOffer(l.server.offerID, sigData); err != nil {
				continue
			}

			// Wait for answer
			answerData, err := signaling.waitForAnswer(l.server.offerID)
			if err != nil {
				continue
			}

			if err := conn.AcceptAnswer(answerData); err != nil {
				continue
			}

			l.connChan <- conn
		}
	}
}

// Accept implements net.Listener
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connChan:
		return conn, nil
	case <-l.ctx.Done():
		return nil, fmt.Errorf("listener closed")
	}
}

// Close implements net.Listener
func (l *Listener) Close() error {
	l.cancel()
	l.mu.Lock()
	if l.currentConn != nil {
		l.currentConn.Close()
	}
	l.mu.Unlock()
	return nil
}

// Addr implements net.Listener
func (l *Listener) Addr() net.Addr {
	return &WebRTCAddr{id: l.server.offerID}
}

// WebRTCAddr implements net.Addr
type WebRTCAddr struct {
	id string
}

func (a *WebRTCAddr) Network() string { return "webrtc" }
func (a *WebRTCAddr) String() string  { return a.id }
