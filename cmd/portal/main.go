package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/InsulaLabs/ferry/pkg/core"
	"github.com/InsulaLabs/ferry/pkg/p2p"
	"github.com/fatih/color"
	"gopkg.in/yaml.v3"
)

type PortalConfig struct {
	ApiKeyEnv  string   `yaml:"api_key_env"`
	Endpoints  []string `yaml:"endpoints"`
	SkipVerify bool     `yaml:"skip_verify"`
	Timeout    string   `yaml:"timeout"`
}

type PortalMode int

const (
	ModeReceiver PortalMode = iota
	ModeSender
)

func main() {
	var (
		configPath string
		mode       string
		sessionID  string
		localAddr  string
	)

	flag.StringVar(&configPath, "config", "", "Path to ferry configuration file")
	flag.StringVar(&mode, "mode", "", "Portal mode: 'receiver' or 'sender'")
	flag.StringVar(&sessionID, "session", "", "P2P session ID")
	flag.StringVar(&localAddr, "local", "", "Local address to proxy (e.g., localhost:8080)")
	flag.Parse()

	// Allow positional arguments for compatibility
	args := flag.Args()
	if mode == "" && len(args) > 0 {
		mode = args[0]
	}
	if sessionID == "" && len(args) > 1 {
		sessionID = args[1]
	}
	if localAddr == "" && len(args) > 2 {
		localAddr = args[2]
	}

	if mode == "" || sessionID == "" || localAddr == "" {
		printUsage()
		os.Exit(1)
	}

	var portalMode PortalMode
	switch strings.ToLower(mode) {
	case "receiver":
		portalMode = ModeReceiver
	case "sender":
		portalMode = ModeSender
	default:
		fmt.Fprintf(os.Stderr, "%s Invalid mode '%s'. Must be 'receiver' or 'sender'\n", color.RedString("Error:"), mode)
		os.Exit(1)
	}

	// Load configuration
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	// Create ferry instance
	ferryConfig := &core.Config{
		ApiKey:     os.Getenv(cfg.ApiKeyEnv),
		Endpoints:  cfg.Endpoints,
		SkipVerify: cfg.SkipVerify,
	}

	if cfg.Timeout != "" {
		timeout, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid timeout format: %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		ferryConfig.Timeout = timeout
	} else {
		ferryConfig.Timeout = 30 * time.Second
	}

	if ferryConfig.ApiKey == "" {
		fmt.Fprintf(os.Stderr, "%s API key environment variable %s is not set\n", color.RedString("Error:"), cfg.ApiKeyEnv)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	f, err := core.New(logger, ferryConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Received signal, shutting down portal")
		cancel()
	}()

	// Run portal
	switch portalMode {
	case ModeReceiver:
		runReceiver(ctx, f, sessionID, localAddr, logger)
	case ModeSender:
		runSender(ctx, f, sessionID, localAddr, logger)
	}
}

func runReceiver(ctx context.Context, f *core.Ferry, sessionID, localAddr string, logger *slog.Logger) {
	logger.Info("Starting portal receiver", "session_id", sessionID, "local_addr", localAddr)

	cacheController := core.GetCacheController[string](f, "")
	events := core.GetEvents(f)

	signaling := p2p.NewCacheSignalingClient(p2p.CacheSignalingConfig{
		CacheController: cacheController,
		Events:          events,
		Logger:          logger,
	})

	logger.Info("Cleaning up any stale session data", "session_id", sessionID)
	signaling.Cleanup(ctx, sessionID)

	logger.Info("Waiting for P2P connection", "session_id", sessionID)

	conn, offerData, err := p2p.Offer(logger.With("role", "receiver"))
	if err != nil {
		logger.Error("Failed to create WebRTC offer", "error", err)
		os.Exit(1)
	}

	if err := signaling.RegisterOffer(ctx, sessionID, offerData); err != nil {
		logger.Error("Failed to register offer", "error", err)
		os.Exit(1)
	}

	logger.Info("Offer registered, waiting for answer", "session_id", sessionID)

	answerData, err := signaling.WaitForAnswer(ctx, sessionID)
	if err != nil {
		logger.Error("Failed to get answer", "error", err)
		os.Exit(1)
	}

	if err := conn.AcceptAnswer(answerData); err != nil {
		logger.Error("Failed to accept answer", "error", err)
		os.Exit(1)
	}

	logger.Info("WebRTC connection established, starting HTTP proxy", "session_id", sessionID)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	hpp := p2p.NewHTTPProxyProtocol(conn, logger)

	if err := hpp.Serve(ctx, localAddr); err != nil {
		logger.Error("HTTP proxy failed", "error", err)
		os.Exit(1)
	}

	signaling.Cleanup(ctx, sessionID)
	conn.Close()
}

func runSender(ctx context.Context, f *core.Ferry, sessionID, localAddr string, logger *slog.Logger) {
	logger.Info("Starting portal sender", "session_id", sessionID, "local_addr", localAddr)

	cacheController := core.GetCacheController[string](f, "")
	events := core.GetEvents(f)

	signaling := p2p.NewCacheSignalingClient(p2p.CacheSignalingConfig{
		CacheController: cacheController,
		Events:          events,
		Logger:          logger,
	})

	logger.Info("Cleaning up any stale session data", "session_id", sessionID)
	signaling.Cleanup(ctx, sessionID)

	logger.Info("Waiting for P2P offer", "session_id", sessionID)

	offerData, err := signaling.WaitForOffer(ctx, sessionID)
	if err != nil {
		logger.Error("Failed to get offer", "error", err)
		os.Exit(1)
	}

	conn, answerData, err := p2p.Answer(logger.With("role", "sender"), offerData)
	if err != nil {
		logger.Error("Failed to create WebRTC answer", "error", err)
		os.Exit(1)
	}

	if err := signaling.SendAnswer(ctx, sessionID, answerData); err != nil {
		logger.Error("Failed to send answer", "error", err)
		conn.Close()
		os.Exit(1)
	}

	logger.Info("WebRTC connection established, starting HTTP proxy", "session_id", sessionID)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	hpp := p2p.NewHTTPProxyProtocol(conn, logger)

	if err := hpp.Connect(ctx, localAddr); err != nil {
		logger.Error("HTTP proxy failed", "error", err)
		os.Exit(1)
	}

	signaling.Cleanup(ctx, sessionID)
	conn.Close()
}

func loadConfig(configPath string) (*PortalConfig, error) {
	// Determine config path
	if configPath == "" {
		// Check ferry.yaml
		if _, err := os.Stat("ferry.yaml"); err == nil {
			configPath = "ferry.yaml"
		} else {
			// Check FERRY_CONFIG env
			if envPath := os.Getenv("FERRY_CONFIG"); envPath != "" {
				configPath = envPath
			} else {
				return nil, fmt.Errorf("no config file found: checked ferry.yaml and FERRY_CONFIG env var")
			}
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	var cfg PortalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Set defaults
	if cfg.ApiKeyEnv == "" {
		cfg.ApiKeyEnv = "INSI_API_KEY"
	}

	return &cfg, nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: portal [flags] <mode> <session-id> <local-addr>\n")
	fmt.Fprintf(os.Stderr, "\nModes:\n")
	fmt.Fprintf(os.Stderr, "  receiver <session-id> <local-addr>    Wait for P2P connection and proxy to local TCP server\n")
	fmt.Fprintf(os.Stderr, "  sender <session-id> <local-addr>      Connect to P2P receiver and proxy from local TCP server\n")
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  portal receiver mysession localhost:8080\n")
	fmt.Fprintf(os.Stderr, "  portal sender mysession localhost:8081\n")
	fmt.Fprintf(os.Stderr, "  portal --config prod.yaml receiver mysession localhost:8080\n")
}
