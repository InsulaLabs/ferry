/*
	The connector is a piece of software that wraps the p2p library
	to model the whole thing as a "call" between two entities.

	When you instantiate a connector, you pass in a callable.

	The connector binds to a signaling server and will wait for calls on the behalf of the callable.
	If a call is received, the connector will call the OnCallRequest method of the callable with the
	caller id and importance level.

	If the callable accepts the call, the connector will call the OnCallAccepted method of the callable
	with the call struct.

	When the callable is done with the call, it can hang up the call by calling the Hangup method of the
	call struct.

	When the connector is done with the call, it will close the connection and the callable will be notified
	of the closure.

	Note:
		The "callable" that is given to the connector will receive all hangup events,
		even ones that they initiated, not just received.




*/

package tekp2p

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/InsulaLabs/ferry/remote/p2p"
	"github.com/google/uuid"
)

const (
	MaxCallableIDLength     = 128 // largest possible caller id length (self imposed limit)
	AssumedIntSizeBytes     = 8   // largest int size in bytes (assumed)
	NotificationBufferBytes = 32  // smallest possible buffer rounded up for the notification (tested)
)

type CallImportance int

const (
	CallImportanceUnknown CallImportance = iota
	CallImportanceLow
	CallImportanceMedium
	CallImportanceHigh
)

type CallEndReason int

const (
	CallEndReasonUnknown CallEndReason = iota
	CallEndReasonHangup
	CallEndReasonTimeout
)

type Callable interface {
	GetID() string
	GetToken() string

	OnCallRequest(notification *CallNotification) bool // accept or reject
	OnCallAccepted(call *Call) error
	OnCallHangup(end *CallEnd) error
}

type Call struct {
	CallUUID  string
	Initiator string
	Recipient string
	conn      net.Conn
	Logger    *slog.Logger
	callable  Callable
	server    *p2p.Server
}

type CallEnd struct {
	Reason    CallEndReason
	CallUUID  string
	Initiator string
	Recipient string
}

type CallNotification struct {
	CallerID   string         `json:"caller_id"`
	Importance CallImportance `json:"importance"`
}

type CallRequest struct {
	RecipientID string         `json:"recipient_id"`
	Importance  CallImportance `json:"importance"`
	SkipVerify  bool           `json:"skip_verify"`
}

type CallResponse struct {
	Accepted bool
}

type PeerConnector struct {
	id           string
	signalingURL string
	logger       *slog.Logger
	callable     Callable
	receiver     *p2p.Server

	listenBufferSize    int
	handShakeBufferSize int
}

type ConnectorConfig struct {
	Logger           *slog.Logger
	SignalingURL     string
	Callable         Callable
	SkipTLS          bool
	ListenBufferSize int
}

func NewConnector(config ConnectorConfig) (*PeerConnector, error) {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	if config.ListenBufferSize <= 10 {
		config.Logger.Warn("listen buffer size is too small, setting to 10")
		config.ListenBufferSize = 10
	}

	if len(config.Callable.GetID()) > MaxCallableIDLength {
		return nil, errors.New("caller id is too long (max 128 characters)")
	}

	node := &PeerConnector{
		id:               config.Callable.GetID(),
		signalingURL:     config.SignalingURL,
		logger:           config.Logger,
		callable:         config.Callable,
		listenBufferSize: config.ListenBufferSize,
	}
	return node, nil
}

// Will run until ctx is cancelled, nonblocking
// when cancelled, the server will be closed and the
// connector will be closed
func (x *PeerConnector) Connect(ctx context.Context) error {
	receiver, err := p2p.NewServer(p2p.ServerConfig{
		Logger:       x.logger,
		Context:      ctx,
		SignalingURL: x.signalingURL,
		OfferID:      x.id,
		Token:        x.callable.GetToken(),
		ListenBuffer: x.listenBufferSize,
	})
	if err != nil {
		x.logger.Error("new server error", "error", err)
		return err
	}
	x.receiver = receiver

	go func() {

		// get the listener and close it on exit
		listener, err := receiver.Listen()
		if err != nil {
			x.logger.Error("listen error", "error", err)
			return
		}
		defer listener.Close()

		/*
			I found out that golang doesn't move allocations out of the
			loops! I had it in there thinking "the compiler can move this"

			Nope. It doesn't.
		*/
		buffer := make([]byte, MaxCallableIDLength+AssumedIntSizeBytes)

		// accept connections and treat them as calls
		// check with the interface to see if the call should be accepted
		// and if so, create a call struct and hand it off to the callable
		for {
			conn, err := listener.Accept()
			if err != nil {
				x.logger.Error("accept error", "error", err)
				continue
			}

			// read tells us "n" so we dont need to reset the buffer
			n, err := conn.Read(buffer)
			if err != nil {
				if err == io.EOF {
					x.logger.Info("connection closed")
					return
				}
				x.logger.Error("read error", "error", err)
				continue
			}

			if n == 0 {
				x.logger.Info("received empty message")
				continue
			}

			var notification CallNotification
			err = json.Unmarshal(buffer[:n], &notification)
			if err != nil {
				x.logger.Error("unmarshal error", "error", err)
				continue
			}

			response := CallResponse{
				Accepted: x.callable.OnCallRequest(&notification),
			}

			x.logger.Info("accepted?", "accepted", response.Accepted)

			responseBytes, err := json.Marshal(response)
			if err != nil {
				x.logger.Error("marshal error", "error", err)
				continue
			}

			conn.Write(responseBytes)

			x.logger.Info("creating call")
			call := &Call{
				CallUUID:  uuid.New().String(),
				Initiator: string(buffer[:n]),
				Recipient: x.id,
				conn:      conn,
				Logger:    x.logger.With("caller_id", string(buffer[:n])),
				server:    receiver,
				callable:  x.callable,
			}

			x.logger.Info("accepting call")

			go func() {
				err = x.callable.OnCallAccepted(call)
				if err != nil {
					x.logger.Error("call error", "error", err)
					call.conn.Close()
					return
				}
			}()
		}
	}()
	return nil
}

func (x *PeerConnector) Call(ctx context.Context, req *CallRequest) (*Call, error) {
	if len(req.RecipientID) > MaxCallableIDLength {
		return nil, errors.New("recipient id is too long (max 128 characters)")
	}

	conn, err := p2p.Dial(p2p.DialConfig{
		Token:        x.callable.GetToken(),
		SignalingURL: x.signalingURL,
		OfferID:      req.RecipientID, // We want to call them so thats the offer
		Logger:       x.logger,
		SkipVerify:   req.SkipVerify,
	})
	if err != nil {
		return nil, err
	}

	notification := &CallNotification{
		CallerID:   x.id,
		Importance: req.Importance,
	}

	notifyBytes, err := json.Marshal(notification)
	if err != nil {
		x.logger.Error("marshal error", "error", err)
		return nil, err
	}

	conn.Write(notifyBytes)

	buffer := make([]byte, NotificationBufferBytes)
	n, err := conn.Read(buffer)
	if err != nil {
		x.logger.Error("read error", "error", err)
		return nil, err
	}

	if n == 0 {
		x.logger.Error("received empty message")
		return nil, errors.New("received empty message")
	}

	var response CallResponse
	err = json.Unmarshal(buffer[:n], &response)
	if err != nil {
		x.logger.Error("unmarshal error", "error", err)
		return nil, err
	}

	if response.Accepted {
		x.logger.Info("(CALLER) our call was accepted - handing call to operator")
		return &Call{
			CallUUID:  uuid.New().String(),
			Initiator: x.id, // we are the initiator
			Recipient: req.RecipientID,
			conn:      conn,
			Logger:    x.logger.With("caller_id", req.RecipientID),
			server:    x.receiver,
			callable:  x.callable,
		}, nil
	}

	x.logger.Info("(CALLER) our call was rejected")
	conn.Close()
	return nil, errors.New("call rejected")
}

func (x *Call) Transmit(p []byte) (n int, err error) {

	// NOTE: Here we could insert a connection bridge
	// and have a stream copied off for logging / recording
	// purposes.

	return x.conn.Write(p)
}

func (x *Call) Receive(p []byte) (n int, err error) {

	// NOTE: Here we could insert a connection bridge
	// and have a stream copied off for logging / recording
	// purposes.

	return x.conn.Read(p)
}

func (x *Call) Hangup() error {
	x.Logger.Info("hanging up call",
		"call_uuid", x.CallUUID,
		"initiator", x.Initiator,
		"recipient", x.Recipient,
	)

	if err := x.conn.Close(); err != nil {
		x.Logger.Error("close error", "error", err)
	}

	if x.callable == nil {
		x.Logger.Error("callable is nil on call hangup", "call_uuid", x.CallUUID)
		return nil
	}

	err := x.callable.OnCallHangup(&CallEnd{
		CallUUID:  x.CallUUID,
		Initiator: x.Initiator,
		Recipient: x.Recipient,
		Reason:    CallEndReasonHangup,
	})
	if err != nil {
		x.Logger.Error("callable error on hangup", "error", err)
	}
	return nil
}
