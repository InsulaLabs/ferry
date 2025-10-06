package tekp2p

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/InsulaLabs/ferry/pkg/p2p"
)

const testString = "this is a test to check if the connection is working"
const (
	alhpaToken = "tok-alpha"
	betaTOken  = "tok-beta"
)

type testNode struct {
	id          string
	token       string
	gotAccepted bool
	gotRequest  bool
	gotHangup   bool
	reqHandler  func(notification *CallNotification) bool
	callHandler func(call *Call) error
}

func (t *testNode) GetID() string {
	return t.id
}

func (t *testNode) GetToken() string {
	return t.token
}

func (t *testNode) OnCallRequest(notification *CallNotification) bool {
	t.gotRequest = true
	return t.reqHandler(notification)
}

func (t *testNode) OnCallAccepted(call *Call) error {
	t.gotAccepted = true
	return t.callHandler(call)
}

func (t *testNode) OnCallHangup(end *CallEnd) error {
	t.gotHangup = true
	fmt.Println("testNode.OnCallHangup()", "end", end)
	return nil
}

func TestCall(t *testing.T) {

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	go func() {
		mux := http.NewServeMux()

		server := p2p.NewSignalingServer(p2p.SignalingServerConfig{
			Logger: logger,
			TokenValidationCb: func(token string) bool {
				// This is cb for token auth
				fmt.Println("token", token)
				return true
			},
			CleanupInterval: 1 * time.Minute,
			OfferTimeout:    5 * time.Minute,
			AnswerTimeout:   30 * time.Second,
		})

		if err := server.GetRouteBinder()(mux); err != nil {
			logger.Error("failed to setup routes", "error", err)
			return
		}

		httpServer := &http.Server{
			Addr:    ":8080", // Use a fixed port for testing
			Handler: mux,
		}

		if err := server.Start(); err != nil {
			logger.Error("failed to start signaling server", "error", err)
			return
		}

		go func() {
			logger.Info("starting HTTP server on :8080")
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("HTTP server error", "error", err)
			}
		}()

		<-ctx.Done()
		httpServer.Shutdown(context.Background())
		if err := server.Stop(); err != nil {
			logger.Error("failed to stop signaling server", "error", err)
		}
	}()

	time.Sleep(1 * time.Second)

	alpha := &testNode{
		id:    "alpha",
		token: alhpaToken,
		reqHandler: func(notification *CallNotification) bool {
			logger.Info("alpha.OnCallRequest() [caller]", "callerId", notification.CallerID, "importance", notification.Importance)
			return false
		},
		callHandler: func(call *Call) error {
			logger.Info("alpha.OnCallAccepted() [caller]", "call", call)
			return nil
		},
	}

	dest := "http://localhost:8080"

	caller, err := NewConnector(
		ConnectorConfig{
			Logger:       logger,
			SignalingURL: dest,
			Callable:     alpha,
		},
	)
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	beta := &testNode{
		id:    "beta",
		token: betaTOken,
		reqHandler: func(notification *CallNotification) bool {
			logger.Info("beta.OnCallRequest() [receiver]", "callerId", notification.CallerID, "importance", notification.Importance)
			return true
		},
		callHandler: func(call *Call) error {
			logger.Info("beta.OnCallAccepted() [receiver]", "call", call)
			n, err := call.Transmit([]byte(fmt.Sprintf("echo: %s", testString)))
			if err != nil {
				return err
			}
			logger.Info("beta.OnCallAccepted() [receiver]", "n", n)

			time.Sleep(1 * time.Second)
			call.Hangup() // we are done here
			return nil
		},
	}
	receiver, err := NewConnector(ConnectorConfig{
		Logger:       logger,
		SignalingURL: dest,
		Callable:     beta,
	})
	if err != nil {
		t.Fatalf("NewConnector() error = %v", err)
	}

	// start the fuckin things
	// ------------------------------------------------------------

	go func() {
		logger.Info("receiver.Connect()")
		if err := receiver.Connect(context.Background()); err != nil {
			t.Fatalf("receiver.Connect() error = %v", err)
		}
	}()

	go func() {
		logger.Info("caller.Connect()")
		if err := caller.Connect(context.Background()); err != nil {
			t.Fatalf("caller.Connect() error = %v", err)
		}
	}()

	time.Sleep(1 * time.Second)

	// write the data
	// ------------------------------------------------------------

	logger.Info("caller.Call()")
	cr, err := caller.Call(context.Background(), &CallRequest{
		RecipientID: "beta",
		Importance:  CallImportanceHigh,
		SkipVerify:  true,
	})
	if err != nil {
		t.Fatalf("caller.Call() error = %v", err)
	}

	logger.Info("cr.Transmit()")
	nOut, err := cr.Transmit([]byte(testString))
	if err != nil {
		t.Fatalf("cr.Transmit() error = %v", err)
	}

	if nOut != len(testString) {
		t.Fatalf("expected %d bytes, got %d", len(testString), nOut)
	}

	time.Sleep(1 * time.Second)

	// receive the echo
	// ------------------------------------------------------------

	buffer := make([]byte, 1024)

	logger.Info("cr.Receive()")
	nIn, err := cr.Receive(buffer)

	if err != nil {
		t.Fatalf("cr.Receive() error = %v", err)
	}

	if string(buffer[:nIn]) != fmt.Sprintf("echo: %s", testString) {
		t.Fatalf("expected '%s', got %s", fmt.Sprintf("echo: %s", testString), buffer[:nIn])
	}

	logger.Info("done")

	cr.Hangup()

	time.Sleep(1 * time.Second)

	if !alpha.gotHangup {
		t.Fatalf("alpha did not receive a hangup event")
	}

	if !beta.gotHangup {
		t.Fatalf("beta did not receive a hangup event")
	}
}
