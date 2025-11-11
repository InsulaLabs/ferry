package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/InsulaLabs/ferry/pkg/core"
	"github.com/InsulaLabs/ferry/pkg/p2p"
	"github.com/InsulaLabs/insi/db/models"
	"github.com/fatih/color"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type FerryConfig struct {
	ApiKeyEnv  string   `yaml:"api_key_env"`
	Endpoints  []string `yaml:"endpoints"`
	SkipVerify bool     `yaml:"skip_verify"`
	Timeout    string   `yaml:"timeout"`
	Domain     string   `yaml:"domain,omitempty"`
}

var (
	logger          *slog.Logger
	configPath      string
	generateFlag    string
	skipOnErrorFlag bool
	confirmFlag     bool
)

func init() {
	logOpts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	handler := slog.NewTextHandler(os.Stderr, logOpts)
	logger = slog.New(handler)

	flag.StringVar(&configPath, "config", "", "Path to the ferry configuration file (defaults to ferry.yaml, then FERRY_CONFIG env)")
	flag.StringVar(&generateFlag, "generate", "", "Generate config from comma-separated endpoints")
	flag.BoolVar(&skipOnErrorFlag, "skip-on-error", false, "Continue without prompting on individual key failures during snapshot")
	flag.BoolVar(&confirmFlag, "confirm", false, "Skip all confirmation prompts (for destructive operations like scrub)")
}

func main() {
	flag.Parse()

	// Handle config generation
	if generateFlag != "" {
		generateConfig(generateFlag)
		return
	}

	// Load config
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("Failed to load configuration", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	// Convert to ferry config
	ferryConfig := &core.Config{
		ApiKey:     os.Getenv(cfg.ApiKeyEnv),
		Endpoints:  cfg.Endpoints,
		SkipVerify: cfg.SkipVerify,
		Domain:     cfg.Domain,
	}

	// Parse timeout
	if cfg.Timeout != "" {
		timeout, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			logger.Error("Invalid timeout format", "timeout", cfg.Timeout, "error", err)
			fmt.Fprintf(os.Stderr, "%s Invalid timeout format: %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		ferryConfig.Timeout = timeout
	} else {
		ferryConfig.Timeout = 30 * time.Second
	}

	if ferryConfig.ApiKey == "" {
		logger.Error("API key not found", "env_var", cfg.ApiKeyEnv)
		fmt.Fprintf(os.Stderr, "%s API key environment variable %s is not set\n", color.RedString("Error:"), cfg.ApiKeyEnv)
		os.Exit(1)
	}

	// Create ferry instance
	f, err := core.New(logger, ferryConfig)
	if err != nil {
		logger.Error("Failed to create ferry instance", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	// Parse commands
	args := flag.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "values":
		handleValues(f, cmdArgs)
	case "cache":
		handleCache(f, cmdArgs)
	case "events":
		handleEvents(f, cmdArgs)
	case "ping":
		handlePing(f, cmdArgs)
	case "blob":
		handleBlob(f, cmdArgs)
	case "p2p":
		handleP2P(f, cmdArgs)
	case "snapshot":
		handleSnapshot(f, cmdArgs)
	case "scrub":
		handleScrub(f, cmdArgs)
	default:
		logger.Error("Unknown command", "command", command)
		printUsage()
		os.Exit(1)
	}
}

func generateConfig(endpoints string) {
	// Split endpoints by comma
	endpointList := strings.Split(endpoints, ",")
	for i, ep := range endpointList {
		endpointList[i] = strings.TrimSpace(ep)
	}

	// Create config
	cfg := FerryConfig{
		ApiKeyEnv:  "INSI_API_KEY",
		Endpoints:  endpointList,
		SkipVerify: false,
		Timeout:    "30s",
	}

	// Marshal to YAML
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		logger.Error("Failed to marshal config", "error", err)
		fmt.Fprintf(os.Stderr, "%s Failed to marshal config: %v\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	// Print to stdout
	fmt.Print(string(data))
}

func loadConfig() (*FerryConfig, error) {
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

	var cfg FerryConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	if cfg.ApiKeyEnv == "" {
		cfg.ApiKeyEnv = "INSI_API_KEY"
	}

	return &cfg, nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: ferry [flags] <command> [args...]\n")
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	flag.PrintDefaults()

	fmt.Fprintf(os.Stderr, "\n%s\n", color.CyanString("Configuration:"))
	fmt.Fprintf(os.Stderr, "  Ferry looks for configuration in this order:\n")
	fmt.Fprintf(os.Stderr, "  1. --config flag (if specified)\n")
	fmt.Fprintf(os.Stderr, "  2. ferry.yaml in current directory\n")
	fmt.Fprintf(os.Stderr, "  3. FERRY_CONFIG environment variable\n")
	fmt.Fprintf(os.Stderr, "  API key is read from the environment variable specified in config (default: INSI_API_KEY)\n")

	fmt.Fprintf(os.Stderr, "\n%s\n", color.CyanString("Commands:"))

	// Config generation
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Configuration:"))
	fmt.Fprintf(os.Stderr, "  %s --generate %s\n", color.GreenString("ferry"), color.CyanString("\"endpoint1,endpoint2,...\""))
	fmt.Fprintf(os.Stderr, "    Generate a ferry.yaml configuration file from comma-separated endpoints\n")
	fmt.Fprintf(os.Stderr, "    Example: ferry --generate \"red.insulalabs.io:443,blue.insulalabs.io:443\" > ferry.yaml\n")

	// Ping
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Connectivity:"))
	fmt.Fprintf(os.Stderr, "  %s\n", color.GreenString("ping"))
	fmt.Fprintf(os.Stderr, "    Test connectivity to the configured endpoints\n")

	// Values operations
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Value Store Operations:"))
	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("values"), color.CyanString("get"), color.CyanString("<key>"))
	fmt.Fprintf(os.Stderr, "    Retrieve a value by key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("values"), color.CyanString("set"), color.CyanString("<key>"), color.CyanString("<value>"))
	fmt.Fprintf(os.Stderr, "    Store a value with the given key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("values"), color.CyanString("setnx"), color.CyanString("<key>"), color.CyanString("<value>"))
	fmt.Fprintf(os.Stderr, "    Set value only if key doesn't exist (set-if-not-exists)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("values"), color.CyanString("delete"), color.CyanString("<key>"))
	fmt.Fprintf(os.Stderr, "    Delete a key-value pair\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s %s\n", color.GreenString("values"), color.CyanString("cas"), color.CyanString("<key>"), color.CyanString("<old_value>"), color.CyanString("<new_value>"))
	fmt.Fprintf(os.Stderr, "    Compare-and-swap: update value only if current value matches old_value\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("values"), color.CyanString("bump"), color.CyanString("<key>"), color.CyanString("<increment>"))
	fmt.Fprintf(os.Stderr, "    Atomically increment a numeric value by the given amount\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s %s\n", color.GreenString("values"), color.CyanString("iterate"), color.CyanString("<prefix>"), color.CyanString("[offset]"), color.CyanString("[limit]"))
	fmt.Fprintf(os.Stderr, "    List keys matching the given prefix (default: offset=0, limit=100)\n")

	// Cache operations
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Cache Operations (volatile storage):"))
	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("cache"), color.CyanString("get"), color.CyanString("<key>"))
	fmt.Fprintf(os.Stderr, "    Retrieve a cached value by key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("cache"), color.CyanString("set"), color.CyanString("<key>"), color.CyanString("<value>"))
	fmt.Fprintf(os.Stderr, "    Store a value in cache with the given key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("cache"), color.CyanString("setnx"), color.CyanString("<key>"), color.CyanString("<value>"))
	fmt.Fprintf(os.Stderr, "    Set value only if key doesn't exist (set-if-not-exists)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("cache"), color.CyanString("delete"), color.CyanString("<key>"))
	fmt.Fprintf(os.Stderr, "    Delete a cached key-value pair\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s %s\n", color.GreenString("cache"), color.CyanString("cas"), color.CyanString("<key>"), color.CyanString("<old_value>"), color.CyanString("<new_value>"))
	fmt.Fprintf(os.Stderr, "    Compare-and-swap: update cached value only if current value matches old_value\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s %s\n", color.GreenString("cache"), color.CyanString("iterate"), color.CyanString("<prefix>"), color.CyanString("[offset]"), color.CyanString("[limit]"))
	fmt.Fprintf(os.Stderr, "    List cached keys matching the given prefix (default: offset=0, limit=100)\n")

	// Events operations
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Event Operations:"))
	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("events"), color.CyanString("publish"), color.CyanString("<topic>"), color.CyanString("<data>"))
	fmt.Fprintf(os.Stderr, "    Publish data to a topic (data can be string or JSON)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("events"), color.CyanString("subscribe"), color.CyanString("<topic>"))
	fmt.Fprintf(os.Stderr, "    Subscribe to a topic and print received events (blocks until Ctrl+C)\n")

	fmt.Fprintf(os.Stderr, "  %s %s\n", color.GreenString("events"), color.CyanString("purge"))
	fmt.Fprintf(os.Stderr, "    Disconnect all event subscriptions for the current API key across all nodes in the cluster\n")

	// Blob operations
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Blob Storage Operations:"))
	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("blob"), color.CyanString("upload"), color.CyanString("<key>"), color.CyanString("<file>"))
	fmt.Fprintf(os.Stderr, "    Upload a file as a blob with the given key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("blob"), color.CyanString("download"), color.CyanString("<key>"), color.CyanString("[output_file]"))
	fmt.Fprintf(os.Stderr, "    Download a blob by key (outputs to stdout if no output file specified)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("blob"), color.CyanString("delete"), color.CyanString("<key>"))
	fmt.Fprintf(os.Stderr, "    Delete a blob by key\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s %s\n", color.GreenString("blob"), color.CyanString("iterate"), color.CyanString("<prefix>"), color.CyanString("[offset]"), color.CyanString("[limit]"))
	fmt.Fprintf(os.Stderr, "    List blob keys matching the given prefix (default: offset=0, limit=100)\n")

	// P2P operations
	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("P2P File Transfer Operations:"))
	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("p2p"), color.CyanString("receive"), color.CyanString("<session-id>"), color.CyanString("<output-file>"))
	fmt.Fprintf(os.Stderr, "    Wait for and receive a file transfer with the given session ID, save to output-file\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s %s\n", color.GreenString("p2p"), color.CyanString("send"), color.CyanString("<session-id>"), color.CyanString("<file>"))
	fmt.Fprintf(os.Stderr, "    Send a file to the receiver waiting with the given session ID\n")

	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Snapshot Operations:"))
	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("snapshot"), color.CyanString("values"), color.CyanString("<name>"))
	fmt.Fprintf(os.Stderr, "    Create a snapshot of all values to <name>/values.db (SQLite database)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("snapshot"), color.CyanString("cache"), color.CyanString("<name>"))
	fmt.Fprintf(os.Stderr, "    Create a snapshot of all cache entries to <name>/cache.db (SQLite database)\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("snapshot"), color.CyanString("blob"), color.CyanString("<name>"))
	fmt.Fprintf(os.Stderr, "    Create a snapshot of all blobs to <name>/blobs/ directory\n")

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.GreenString("snapshot"), color.CyanString("full"), color.CyanString("<name>"))
	fmt.Fprintf(os.Stderr, "    Create a complete snapshot of values, cache, and blobs to <name>/ directory\n")

	fmt.Fprintf(os.Stderr, "\n  %s\n", color.MagentaString("Snapshot Flags:"))
	fmt.Fprintf(os.Stderr, "    %s    Continue without prompting when individual keys fail to fetch\n", color.CyanString("--skip-on-error"))

	fmt.Fprintf(os.Stderr, "\n%s\n", color.YellowString("Scrub Operations (DESTRUCTIVE):"))
	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.RedString("scrub"), color.CyanString("values"), color.CyanString("[--confirm]"))
	fmt.Fprintf(os.Stderr, "    %s Delete ALL values from the remote database\n", color.RedString("⚠"))

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.RedString("scrub"), color.CyanString("cache"), color.CyanString("[--confirm]"))
	fmt.Fprintf(os.Stderr, "    %s Delete ALL cache entries from the remote database\n", color.RedString("⚠"))

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.RedString("scrub"), color.CyanString("blob"), color.CyanString("[--confirm]"))
	fmt.Fprintf(os.Stderr, "    %s Delete ALL blobs from the remote database\n", color.RedString("⚠"))

	fmt.Fprintf(os.Stderr, "  %s %s %s\n", color.RedString("scrub"), color.CyanString("full"), color.CyanString("[--confirm]"))
	fmt.Fprintf(os.Stderr, "    %s Delete ALL values, cache, and blobs from the remote database\n", color.RedString("⚠"))

	fmt.Fprintf(os.Stderr, "\n  %s\n", color.MagentaString("Scrub Flags:"))
	fmt.Fprintf(os.Stderr, "    %s          Skip all confirmation prompts (use with extreme caution!)\n", color.CyanString("--confirm"))

	// Examples
	fmt.Fprintf(os.Stderr, "\n%s\n", color.CyanString("Examples:"))
	fmt.Fprintf(os.Stderr, "  # Generate configuration\n")
	fmt.Fprintf(os.Stderr, "  ferry --generate \"red.insulalabs.io:443,blue.insulalabs.io:443,green.insulalabs.io:443\" > ferry.yaml\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Test connectivity\n")
	fmt.Fprintf(os.Stderr, "  ferry ping\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Value store operations\n")
	fmt.Fprintf(os.Stderr, "  ferry values set user:123 '{\"name\":\"Alice\",\"age\":30}'\n")
	fmt.Fprintf(os.Stderr, "  ferry values set user:123 'Alice'\n")
	fmt.Fprintf(os.Stderr, "  ferry values get user:123\n")
	fmt.Fprintf(os.Stderr, "  ferry values setnx lock:process \"locked\"\n")
	fmt.Fprintf(os.Stderr, "  ferry values bump counter 1\n")
	fmt.Fprintf(os.Stderr, "  ferry values iterate user: 0 50\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Cache operations\n")
	fmt.Fprintf(os.Stderr, "  ferry cache set session:abc \"active\"\n")
	fmt.Fprintf(os.Stderr, "  ferry cache setnx lock:resource \"locked\"\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Event pub/sub\n")
	fmt.Fprintf(os.Stderr, "  ferry events publish notifications '{\"type\":\"alert\",\"message\":\"Hello\"}'\n")
	fmt.Fprintf(os.Stderr, "  ferry events subscribe notifications\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Blob storage\n")
	fmt.Fprintf(os.Stderr, "  ferry blob upload document:123 report.pdf\n")
	fmt.Fprintf(os.Stderr, "  ferry blob download document:123 downloaded-report.pdf\n")
	fmt.Fprintf(os.Stderr, "  ferry blob iterate document: 0 50\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # P2P file transfer\n")
	fmt.Fprintf(os.Stderr, "  ferry p2p receive mysession123 received-file.zip\n")
	fmt.Fprintf(os.Stderr, "  ferry p2p send mysession123 myfile.zip\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Snapshot operations\n")
	fmt.Fprintf(os.Stderr, "  ferry snapshot values mybackup\n")
	fmt.Fprintf(os.Stderr, "  ferry snapshot cache cache-snapshot-2024\n")
	fmt.Fprintf(os.Stderr, "  ferry snapshot blob blob-backup\n")
	fmt.Fprintf(os.Stderr, "  ferry snapshot full full-backup-2024-01-15\n")
	fmt.Fprintf(os.Stderr, "  ferry --skip-on-error snapshot full production-snapshot\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Scrub operations (DESTRUCTIVE - requires confirmation)\n")
	fmt.Fprintf(os.Stderr, "  ferry scrub values              # Prompts for confirmation\n")
	fmt.Fprintf(os.Stderr, "  ferry scrub cache               # Prompts for confirmation\n")
	fmt.Fprintf(os.Stderr, "  ferry scrub blob                # Prompts for confirmation\n")
	fmt.Fprintf(os.Stderr, "  ferry --confirm scrub full      # No prompts, deletes everything!\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  # Using custom config\n")
	fmt.Fprintf(os.Stderr, "  ferry --config prod-ferry.yaml values get mykey\n")

	fmt.Fprintf(os.Stderr, "\n%s\n", color.CyanString("Notes:"))
	fmt.Fprintf(os.Stderr, "  - Value store provides persistent storage with strong consistency\n")
	fmt.Fprintf(os.Stderr, "  - Cache provides fast volatile storage (data may be evicted)\n")
	fmt.Fprintf(os.Stderr, "  - Blob storage provides persistent storage for large binary objects\n")
	fmt.Fprintf(os.Stderr, "  - Events enable real-time pub/sub messaging between clients\n")
	fmt.Fprintf(os.Stderr, "  - P2P enables direct file transfer between ferry instances using WebRTC\n")
	fmt.Fprintf(os.Stderr, "  - Snapshots create local copies of remote data in SQLite databases and filesystem\n")
	fmt.Fprintf(os.Stderr, "  - %s Scrub operations are DESTRUCTIVE and PERMANENT - use with extreme caution!\n", color.RedString("⚠"))
	fmt.Fprintf(os.Stderr, "  - All operations use the ferry package which provides automatic retries and error handling\n")
}

func handlePing(f *core.Ferry, args []string) {
	if len(args) != 0 {
		logger.Error("ping: does not take arguments")
		printUsage()
		os.Exit(1)
	}

	err := f.Ping(3, 1*time.Second)
	if err != nil {
		logger.Error("Ping failed", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}
	color.HiGreen("OK - Ping successful")
}

func handleValues(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("values: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	vc := core.GetValueController[string](f, "")

	subCommand := args[0]
	subArgs := args[1:]

	switch subCommand {
	case "get":
		if len(subArgs) != 1 {
			logger.Error("values get: requires <key>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		value, err := vc.Get(ctx, key)
		if err != nil {
			if err == core.ErrKeyNotFound {
				fmt.Fprintf(os.Stderr, "%s Key '%s' not found.\n", color.RedString("Error:"), color.CyanString(key))
			} else {
				logger.Error("Get failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		fmt.Println(value)

	case "set":
		if len(subArgs) != 2 {
			logger.Error("values set: requires <key> <value>")
			printUsage()
			os.Exit(1)
		}
		key, value := subArgs[0], subArgs[1]
		err := vc.Set(ctx, key, value)
		if err != nil {
			logger.Error("Set failed", "key", key, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "setnx":
		if len(subArgs) != 2 {
			logger.Error("values setnx: requires <key> <value>")
			printUsage()
			os.Exit(1)
		}
		key, value := subArgs[0], subArgs[1]
		err := vc.SetNX(ctx, key, value)
		if err != nil {
			if err == core.ErrConflict {
				fmt.Fprintf(os.Stderr, "%s Key '%s' already exists.\n", color.RedString("Conflict:"), color.CyanString(key))
			} else {
				logger.Error("SetNX failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "delete":
		if len(subArgs) != 1 {
			logger.Error("values delete: requires <key>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		err := vc.Delete(ctx, key)
		if err != nil {
			logger.Error("Delete failed", "key", key, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "cas":
		if len(subArgs) != 3 {
			logger.Error("values cas: requires <key> <old_value> <new_value>")
			printUsage()
			os.Exit(1)
		}
		key, oldValue, newValue := subArgs[0], subArgs[1], subArgs[2]
		err := vc.CompareAndSwap(ctx, key, oldValue, newValue)
		if err != nil {
			if err == core.ErrConflict {
				fmt.Fprintf(os.Stderr, "%s Compare-and-swap failed for key '%s'.\n", color.RedString("Conflict:"), color.CyanString(key))
			} else {
				logger.Error("CAS failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "bump":
		if len(subArgs) != 2 {
			logger.Error("values bump: requires <key> <value>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		value, err := strconv.Atoi(subArgs[1])
		if err != nil {
			logger.Error("bump: value must be an integer", "error", err)
			fmt.Fprintf(os.Stderr, "%s Value must be an integer: %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		err = vc.Bump(ctx, key, value)
		if err != nil {
			logger.Error("Bump failed", "key", key, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "iterate":
		handleValuesIterate(ctx, vc, subArgs)

	default:
		logger.Error("values: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleValuesIterate(ctx context.Context, vc core.ValueController[string], args []string) {
	if len(args) < 1 {
		logger.Error("values iterate: requires <prefix> [offset] [limit]")
		printUsage()
		os.Exit(1)
	}
	prefix := args[0]
	offset, limit := 0, 100

	var err error
	if len(args) > 1 {
		offset, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid offset '%s': %v\n", color.RedString("Error:"), args[1], err)
			os.Exit(1)
		}
	}
	if len(args) > 2 {
		limit, err = strconv.Atoi(args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid limit '%s': %v\n", color.RedString("Error:"), args[2], err)
			os.Exit(1)
		}
	}

	results, err := vc.IterateByPrefix(ctx, prefix, offset, limit)
	if err != nil {
		if err == core.ErrKeyNotFound {
			color.HiRed("No keys found.")
		} else {
			logger.Error("Iterate failed", "prefix", prefix, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}
	for _, item := range results {
		decoded, err := url.QueryUnescape(item)
		if err != nil {
			decoded = item
		}
		fmt.Println(decoded)
	}
}

func handleCache(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("cache: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	cc := core.GetCacheController[string](f, "")

	subCommand := args[0]
	subArgs := args[1:]

	switch subCommand {
	case "get":
		if len(subArgs) != 1 {
			logger.Error("cache get: requires <key>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		value, err := cc.Get(ctx, key)
		if err != nil {
			if err == core.ErrKeyNotFound {
				fmt.Fprintf(os.Stderr, "%s Key '%s' not found in cache.\n", color.RedString("Error:"), color.CyanString(key))
			} else {
				logger.Error("Cache get failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		fmt.Println(value)

	case "set":
		if len(subArgs) != 2 {
			logger.Error("cache set: requires <key> <value>")
			printUsage()
			os.Exit(1)
		}
		key, value := subArgs[0], subArgs[1]
		err := cc.Set(ctx, key, value)
		if err != nil {
			logger.Error("Cache set failed", "key", key, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "setnx":
		if len(subArgs) != 2 {
			logger.Error("cache setnx: requires <key> <value>")
			printUsage()
			os.Exit(1)
		}
		key, value := subArgs[0], subArgs[1]
		err := cc.SetNX(ctx, key, value)
		if err != nil {
			if err == core.ErrConflict {
				fmt.Fprintf(os.Stderr, "%s Key '%s' already exists in cache.\n", color.RedString("Conflict:"), color.CyanString(key))
			} else {
				logger.Error("Cache SetNX failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "delete":
		if len(subArgs) != 1 {
			logger.Error("cache delete: requires <key>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		err := cc.Delete(ctx, key)
		if err != nil {
			if err == core.ErrKeyNotFound {
				fmt.Fprintf(os.Stderr, "%s Key '%s' not found in cache. Nothing to delete.\n", color.YellowString("Warning:"), color.CyanString(key))
			} else {
				logger.Error("Cache delete failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "cas":
		if len(subArgs) != 3 {
			logger.Error("cache cas: requires <key> <old_value> <new_value>")
			printUsage()
			os.Exit(1)
		}
		key, oldValue, newValue := subArgs[0], subArgs[1], subArgs[2]
		err := cc.CompareAndSwap(ctx, key, oldValue, newValue)
		if err != nil {
			if err == core.ErrConflict {
				fmt.Fprintf(os.Stderr, "%s Cache compare-and-swap failed for key '%s'.\n", color.RedString("Conflict:"), color.CyanString(key))
			} else {
				logger.Error("Cache CAS failed", "key", key, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			}
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "iterate":
		handleCacheIterate(ctx, cc, subArgs)

	default:
		logger.Error("cache: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleCacheIterate(ctx context.Context, cc core.CacheController[string], args []string) {
	if len(args) < 1 {
		logger.Error("cache iterate: requires <prefix> [offset] [limit]")
		printUsage()
		os.Exit(1)
	}
	prefix := args[0]
	offset, limit := 0, 100

	var err error
	if len(args) > 1 {
		offset, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid offset '%s': %v\n", color.RedString("Error:"), args[1], err)
			os.Exit(1)
		}
	}
	if len(args) > 2 {
		limit, err = strconv.Atoi(args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid limit '%s': %v\n", color.RedString("Error:"), args[2], err)
			os.Exit(1)
		}
	}

	results, err := cc.IterateByPrefix(ctx, prefix, offset, limit)
	if err != nil {
		if err == core.ErrKeyNotFound {
			color.HiRed("No keys found in cache.")
		} else {
			logger.Error("Cache iterate failed", "prefix", prefix, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}
	for _, item := range results {
		decoded, err := url.QueryUnescape(item)
		if err != nil {
			decoded = item
		}
		fmt.Println(decoded)
	}
}

func handleEvents(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("events: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	events := core.GetEvents(f)

	subCommand := args[0]
	subArgs := args[1:]

	switch subCommand {
	case "publish":
		if len(subArgs) != 2 {
			logger.Error("events publish: requires <topic> <data>")
			printUsage()
			os.Exit(1)
		}
		topic := subArgs[0]
		dataStr := subArgs[1]

		var dataToPublish any
		var jsonData any
		if err := json.Unmarshal([]byte(dataStr), &jsonData); err == nil {
			dataToPublish = jsonData
		} else {
			dataToPublish = dataStr
		}

		publisher := events.GetPublisher(topic)
		err := publisher.Publish(ctx, dataToPublish)
		if err != nil {
			logger.Error("Publish failed", "topic", topic, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK")

	case "subscribe":
		if len(subArgs) != 1 {
			logger.Error("events subscribe: requires <topic>")
			printUsage()
			os.Exit(1)
		}
		topic := subArgs[0]

		sigCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		go func() {
			sig := <-sigChan
			logger.Info("Received signal, stopping subscription...", "signal", sig.String())
			cancel()
		}()

		handler := func(event models.EventPayload) {
			fmt.Printf("[%s] Received event on topic '%s': %+v\n",
				time.Now().Format("15:04:05"),
				color.CyanString(topic),
				event.Data)
		}

		subscriber := events.GetSubscriber(topic)
		logger.Info("Subscribing to events", "topic", color.CyanString(topic))
		err := subscriber.Subscribe(sigCtx, handler)
		if err != nil {
			if err == context.Canceled {
				logger.Info("Subscription cancelled gracefully.", "topic", color.CyanString(topic))
			} else if err == core.ErrSubscriberLimitExceeded {
				fmt.Fprintf(os.Stderr, "%s Subscriber limit exceeded\n", color.RedString("Error:"))
				os.Exit(1)
			} else {
				logger.Error("Subscription failed", "topic", topic, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
				os.Exit(1)
			}
		}
		logger.Info("Subscription ended.", "topic", color.CyanString(topic))

	case "purge":
		if len(subArgs) != 0 {
			logger.Error("events purge: does not take arguments")
			printUsage()
			os.Exit(1)
		}

		logger.Info("Purging all event subscriptions for current API key across all nodes")
		disconnectedCount, err := events.PurgeAllNodes(ctx)
		if err != nil {
			logger.Error("Purge failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}

		if disconnectedCount == 0 {
			color.HiYellow("No active event subscriptions found to purge.")
		} else {
			color.HiGreen("Successfully purged %d event subscription(s) across all nodes.", disconnectedCount)
		}

	default:
		logger.Error("events: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleBlob(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("blob: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	bc := core.GetBlobController(f)

	subCommand := args[0]
	subArgs := args[1:]

	switch subCommand {
	case "upload":
		if len(subArgs) != 2 {
			logger.Error("blob upload: requires <key> <file>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		filePath := subArgs[1]

		file, err := os.Open(filePath)
		if err != nil {
			logger.Error("Failed to open file", "file", filePath, "error", err)
			fmt.Fprintf(os.Stderr, "%s Failed to open file: %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		defer file.Close()

		fileInfo, err := file.Stat()
		if err != nil {
			logger.Error("Failed to stat file", "file", filePath, "error", err)
			fmt.Fprintf(os.Stderr, "%s Failed to stat file: %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}

		err = bc.Upload(ctx, key, file, fileInfo.Name())
		if err != nil {
			logger.Error("Upload failed", "key", key, "file", filePath, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK - Uploaded %s as blob key '%s'", filePath, key)

	case "download":
		if len(subArgs) < 1 || len(subArgs) > 2 {
			logger.Error("blob download: requires <key> [output_file]")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]

		if len(subArgs) == 2 {
			outputPath := subArgs[1]

			reader, err := bc.Download(ctx, key)
			if err != nil {
				if err == core.ErrKeyNotFound {
					fmt.Fprintf(os.Stderr, "%s Blob key '%s' not found.\n", color.RedString("Error:"), color.CyanString(key))
				} else {
					logger.Error("Download failed", "key", key, "error", err)
					fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
				}
				os.Exit(1)
			}
			defer reader.Close()

			file, err := os.Create(outputPath)
			if err != nil {
				logger.Error("Failed to create output file", "file", outputPath, "error", err)
				fmt.Fprintf(os.Stderr, "%s Failed to create output file: %v\n", color.RedString("Error:"), err)
				os.Exit(1)
			}
			defer file.Close()

			_, err = io.Copy(file, reader)
			if err != nil {
				logger.Error("Failed to write output file", "file", outputPath, "error", err)
				fmt.Fprintf(os.Stderr, "%s Failed to write output file: %v\n", color.RedString("Error:"), err)
				os.Exit(1)
			}
			color.HiGreen("OK - Downloaded blob key '%s' to %s", key, outputPath)
		} else {
			reader, err := bc.Download(ctx, key)
			if err != nil {
				if err == core.ErrKeyNotFound {
					fmt.Fprintf(os.Stderr, "%s Blob key '%s' not found.\n", color.RedString("Error:"), color.CyanString(key))
				} else {
					logger.Error("Download failed", "key", key, "error", err)
					fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
				}
				os.Exit(1)
			}
			defer reader.Close()

			_, err = io.Copy(os.Stdout, reader)
			if err != nil {
				logger.Error("Failed to write to stdout", "error", err)
				fmt.Fprintf(os.Stderr, "%s Failed to write to stdout: %v\n", color.RedString("Error:"), err)
				os.Exit(1)
			}
		}

	case "delete":
		if len(subArgs) != 1 {
			logger.Error("blob delete: requires <key>")
			printUsage()
			os.Exit(1)
		}
		key := subArgs[0]
		err := bc.Delete(ctx, key)
		if err != nil {
			logger.Error("Delete failed", "key", key, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		color.HiGreen("OK - Deleted blob key '%s'", key)

	case "iterate":
		handleBlobIterate(ctx, bc, subArgs)

	default:
		logger.Error("blob: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleBlobIterate(ctx context.Context, bc core.BlobController, args []string) {
	if len(args) < 1 {
		logger.Error("blob iterate: requires <prefix> [offset] [limit]")
		printUsage()
		os.Exit(1)
	}
	prefix := args[0]
	offset, limit := 0, 100

	var err error
	if len(args) > 1 {
		offset, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid offset '%s': %v\n", color.RedString("Error:"), args[1], err)
			os.Exit(1)
		}
	}
	if len(args) > 2 {
		limit, err = strconv.Atoi(args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid limit '%s': %v\n", color.RedString("Error:"), args[2], err)
			os.Exit(1)
		}
	}

	results, err := bc.IterateByPrefix(ctx, prefix, offset, limit)
	if err != nil {
		if err == core.ErrKeyNotFound {
			color.HiRed("No blob keys found.")
		} else {
			logger.Error("Iterate failed", "prefix", prefix, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}
	for _, item := range results {
		decoded, err := url.QueryUnescape(item)
		if err != nil {
			decoded = item
		}
		fmt.Println(decoded)
	}
}

func handleP2P(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("p2p: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	subCommand := args[0]
	subArgs := args[1:]

	switch subCommand {
	case "receive":
		if len(subArgs) != 2 {
			logger.Error("p2p receive: requires <session-id> <output-file>")
			printUsage()
			os.Exit(1)
		}
		sessionID := subArgs[0]
		outputFile := subArgs[1]
		handleP2PReceive(f, sessionID, outputFile)

	case "send":
		if len(subArgs) != 2 {
			logger.Error("p2p send: requires <session-id> <file>")
			printUsage()
			os.Exit(1)
		}
		sessionID := subArgs[0]
		filePath := subArgs[1]
		handleP2PSend(f, sessionID, filePath)

	default:
		logger.Error("p2p: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleP2PReceive(f *core.Ferry, sessionID, outputFile string) {
	ctx := context.Background()
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
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if err := signaling.RegisterOffer(ctx, sessionID, offerData); err != nil {
		logger.Error("Failed to register offer", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	logger.Info("Offer registered, waiting for answer", "session_id", sessionID)

	answerData, err := signaling.WaitForAnswer(ctx, sessionID)
	if err != nil {
		logger.Error("Failed to get answer", "error", err)
		signaling.Cleanup(ctx, sessionID) // Cleanup on error
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if err := conn.AcceptAnswer(answerData); err != nil {
		logger.Error("Failed to accept answer", "error", err)
		signaling.Cleanup(ctx, sessionID)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	logger.Info("WebRTC connection established, receiving file", "session_id", sessionID)

	ftp := p2p.NewFileTransferProtocol(conn, logger)

	// Receive file and save to specified output file
	if err := ftp.ReceiveFile(outputFile); err != nil {
		logger.Error("Failed to receive file", "error", err)
		signaling.Cleanup(ctx, sessionID)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	signaling.Cleanup(ctx, sessionID)
	conn.Close()

	color.HiGreen("File received successfully")
}

func handleP2PSend(f *core.Ferry, sessionID, filePath string) {
	ctx := context.Background()
	cacheController := core.GetCacheController[string](f, "")
	events := core.GetEvents(f)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		logger.Error("File does not exist", "file", filePath)
		fmt.Fprintf(os.Stderr, "%s File does not exist: %s\n", color.RedString("Error:"), filePath)
		os.Exit(1)
	}

	signaling := p2p.NewCacheSignalingClient(p2p.CacheSignalingConfig{
		CacheController: cacheController,
		Events:          events,
		Logger:          logger,
	})

	logger.Info("Cleaning up any stale session data", "session_id", sessionID)
	signaling.Cleanup(ctx, sessionID)

	logger.Info("Waiting for P2P offer", "session_id", sessionID, "file", filePath)

	offerData, err := signaling.WaitForOffer(ctx, sessionID)
	if err != nil {
		logger.Error("Failed to get offer", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	conn, answerData, err := p2p.Answer(logger.With("role", "sender"), offerData)
	if err != nil {
		logger.Error("Failed to create WebRTC answer", "error", err)
		signaling.Cleanup(ctx, sessionID)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if err := signaling.SendAnswer(ctx, sessionID, answerData); err != nil {
		logger.Error("Failed to send answer", "error", err)
		signaling.Cleanup(ctx, sessionID)
		conn.Close()
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	logger.Info("WebRTC connection established, sending file", "session_id", sessionID)

	ftp := p2p.NewFileTransferProtocol(conn, logger)

	if err := ftp.SendFile(filePath); err != nil {
		logger.Error("Failed to send file", "error", err)
		signaling.Cleanup(ctx, sessionID)
		conn.Close()
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	signaling.Cleanup(ctx, sessionID)
	conn.Close()

	color.HiGreen("File sent successfully")
}

func createSQLiteDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS kv (
		key TEXT PRIMARY KEY,
		value BLOB
	);`

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return db, nil
}

func insertKV(db *sql.DB, key, value string) error {
	stmt, err := db.Prepare("INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	if _, err := stmt.Exec(key, value); err != nil {
		return fmt.Errorf("failed to insert key-value: %w", err)
	}

	return nil
}

func safeBlobPath(key, blobsDir string) (string, bool) {
	cleanKey := filepath.Clean(key)

	if strings.Contains(cleanKey, "..") {
		return filepath.Join(blobsDir, strings.ReplaceAll(key, "/", "_")), false
	}

	if filepath.IsAbs(cleanKey) {
		cleanKey = strings.TrimPrefix(cleanKey, "/")
	}

	fullPath := filepath.Join(blobsDir, cleanKey)

	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return filepath.Join(blobsDir, strings.ReplaceAll(key, "/", "_")), false
	}

	absBlobsDir, err := filepath.Abs(blobsDir)
	if err != nil {
		return filepath.Join(blobsDir, strings.ReplaceAll(key, "/", "_")), false
	}

	if !strings.HasPrefix(absFullPath, absBlobsDir+string(filepath.Separator)) {
		return filepath.Join(blobsDir, strings.ReplaceAll(key, "/", "_")), false
	}

	return fullPath, true
}

func promptContinue(message string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintf(os.Stderr, "%s %s [y/N]: ", color.YellowString("⚠"), message)

	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

func handleSnapshot(f *core.Ferry, args []string) {
	if len(args) < 2 {
		logger.Error("snapshot: requires <type> <name>")
		printUsage()
		os.Exit(1)
	}

	snapshotType := args[0]
	snapshotName := args[1]

	if _, err := os.Stat(snapshotName); err == nil {
		logger.Error("Snapshot directory already exists", "directory", snapshotName)
		fmt.Fprintf(os.Stderr, "%s Snapshot directory '%s' already exists\n", color.RedString("Error:"), snapshotName)
		os.Exit(1)
	}

	if err := os.MkdirAll(snapshotName, 0755); err != nil {
		logger.Error("Failed to create snapshot directory", "directory", snapshotName, "error", err)
		fmt.Fprintf(os.Stderr, "%s Failed to create directory: %v\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	ctx := context.Background()

	switch snapshotType {
	case "values":
		if err := snapshotValues(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Values snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "cache":
		if err := snapshotCache(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Cache snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "blob":
		if err := snapshotBlobs(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Blob snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "full":
		if err := snapshotValues(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Values snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		if err := snapshotCache(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Cache snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		if err := snapshotBlobs(ctx, f, snapshotName, skipOnErrorFlag); err != nil {
			logger.Error("Blob snapshot failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	default:
		logger.Error("Invalid snapshot type", "type", snapshotType)
		fmt.Fprintf(os.Stderr, "%s Invalid snapshot type '%s'. Must be one of: values, cache, blob, full\n", color.RedString("Error:"), snapshotType)
		os.Exit(1)
	}

	color.HiGreen("✓ Snapshot complete: %s", snapshotName)
}

func snapshotValues(ctx context.Context, f *core.Ferry, dir string, skipOnError bool) error {
	color.HiCyan("Starting values snapshot...")

	dbPath := filepath.Join(dir, "values.db")
	db, err := createSQLiteDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to create values database: %w", err)
	}
	defer db.Close()

	vc := core.GetValueController[string](f, "")

	offset := 0
	limit := 100
	totalKeys := 0

	for {
		keys, err := vc.IterateByPrefix(ctx, "*", offset, limit)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate values: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			value, err := vc.Get(ctx, key)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to fetch value for key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			if err := insertKV(db, key, string(value)); err != nil {
				errMsg := fmt.Sprintf("Failed to save value for key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			totalKeys++
		}

		color.Cyan("Snapshotted %d values...", totalKeys)

		if len(keys) < limit {
			break
		}

		offset += limit
		time.Sleep(250 * time.Millisecond)
	}

	color.HiGreen("✓ Values snapshot complete: %d keys", totalKeys)
	return nil
}

func snapshotCache(ctx context.Context, f *core.Ferry, dir string, skipOnError bool) error {
	color.HiCyan("Starting cache snapshot...")

	dbPath := filepath.Join(dir, "cache.db")
	db, err := createSQLiteDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to create cache database: %w", err)
	}
	defer db.Close()

	cc := core.GetCacheController[string](f, "")

	offset := 0
	limit := 100
	totalKeys := 0

	for {
		keys, err := cc.IterateByPrefix(ctx, "*", offset, limit)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate cache: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			value, err := cc.Get(ctx, key)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to fetch cache value for key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			if err := insertKV(db, key, string(value)); err != nil {
				errMsg := fmt.Sprintf("Failed to save cache value for key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			totalKeys++
		}

		color.Cyan("Snapshotted %d cache entries...", totalKeys)

		if len(keys) < limit {
			break
		}

		offset += limit
		time.Sleep(250 * time.Millisecond)
	}

	color.HiGreen("✓ Cache snapshot complete: %d keys", totalKeys)
	return nil
}

func snapshotBlobs(ctx context.Context, f *core.Ferry, dir string, skipOnError bool) error {
	color.HiCyan("Starting blob snapshot...")

	blobsDir := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobsDir, 0755); err != nil {
		return fmt.Errorf("failed to create blobs directory: %w", err)
	}

	bc := core.GetBlobController(f)

	offset := 0
	limit := 100
	totalKeys := 0

	for {
		keys, err := bc.IterateByPrefix(ctx, "*", offset, limit)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate blobs: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			blobPath, safe := safeBlobPath(key, blobsDir)
			if !safe {
				color.Yellow("⚠ Unsafe blob path for key '%s', flattening to: %s", key, filepath.Base(blobPath))
			}

			blobDir := filepath.Dir(blobPath)
			if err := os.MkdirAll(blobDir, 0755); err != nil {
				errMsg := fmt.Sprintf("Failed to create directory for blob key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			reader, err := bc.Download(ctx, key)
			if err != nil {
				errMsg := fmt.Sprintf("Failed to download blob for key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			file, err := os.Create(blobPath)
			if err != nil {
				reader.Close()
				errMsg := fmt.Sprintf("Failed to create file for blob key '%s': %v", key, err)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			_, copyErr := io.Copy(file, reader)
			file.Close()
			reader.Close()

			if copyErr != nil {
				errMsg := fmt.Sprintf("Failed to write blob for key '%s': %v", key, copyErr)
				color.Yellow("⚠ %s", errMsg)

				if !skipOnError {
					if !promptContinue("Continue with snapshot?") {
						return fmt.Errorf("snapshot aborted by user")
					}
				}
				continue
			}

			totalKeys++
		}

		color.Cyan("Snapshotted %d blobs...", totalKeys)

		if len(keys) < limit {
			break
		}

		offset += limit
		time.Sleep(250 * time.Millisecond)
	}

	color.HiGreen("✓ Blob snapshot complete: %d keys", totalKeys)
	return nil
}

func promptConfirmScrub(category string, approxCount int) bool {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintf(os.Stderr, "\n%s %s\n", color.RedString("⚠ WARNING:"), color.HiYellowString("You are about to delete ALL %s from the remote database.", category))
	if approxCount > 0 {
		fmt.Fprintf(os.Stderr, "%s Found approximately %s to delete.\n", color.RedString("⚠"), color.HiRedString("%d keys", approxCount))
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("⚠"), color.YellowString("This operation cannot be undone!"))
	fmt.Fprintf(os.Stderr, "\nType '%s' to proceed: ", color.HiCyanString("yes"))

	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	input = strings.TrimSpace(input)
	return input == "yes"
}

func handleScrub(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("scrub: requires <type>")
		printUsage()
		os.Exit(1)
	}

	scrubType := args[0]
	ctx := context.Background()

	switch scrubType {
	case "values":
		if err := scrubValues(ctx, f, confirmFlag); err != nil {
			logger.Error("Values scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "cache":
		if err := scrubCache(ctx, f, confirmFlag); err != nil {
			logger.Error("Cache scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "blob":
		if err := scrubBlobs(ctx, f, confirmFlag); err != nil {
			logger.Error("Blob scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	case "full":
		if err := scrubValues(ctx, f, confirmFlag); err != nil {
			logger.Error("Values scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		if err := scrubCache(ctx, f, confirmFlag); err != nil {
			logger.Error("Cache scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
		if err := scrubBlobs(ctx, f, confirmFlag); err != nil {
			logger.Error("Blob scrub failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %v\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	default:
		logger.Error("Invalid scrub type", "type", scrubType)
		fmt.Fprintf(os.Stderr, "%s Invalid scrub type '%s'. Must be one of: values, cache, blob, full\n", color.RedString("Error:"), scrubType)
		os.Exit(1)
	}

	color.HiGreen("✓ Scrub complete")
}

func scrubValues(ctx context.Context, f *core.Ferry, confirm bool) error {
	vc := core.GetValueController[string](f, "")

	if !confirm {
		keys, err := vc.IterateByPrefix(ctx, "*", 0, 100)
		approxCount := 0
		if err == nil {
			approxCount = len(keys)
		}

		if !promptConfirmScrub("values", approxCount) {
			color.Yellow("Values scrub cancelled by user")
			return fmt.Errorf("operation cancelled by user")
		}
	}

	color.HiCyan("Starting values scrub...")

	totalDeleted := 0
	failedKeys := 0

	for {
		keys, err := vc.IterateByPrefix(ctx, "*", 0, 100)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate values: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			if err := vc.Delete(ctx, key); err != nil {
				color.Yellow("⚠ Failed to delete key '%s': %v", key, err)
				failedKeys++
				continue
			}
			totalDeleted++
		}

		color.Cyan("Deleted %d values...", totalDeleted)
	}

	if failedKeys > 0 {
		color.HiYellow("✓ Deleted %d values (%d failed)", totalDeleted, failedKeys)
	} else {
		color.HiGreen("✓ Deleted %d total values", totalDeleted)
	}
	return nil
}

func scrubCache(ctx context.Context, f *core.Ferry, confirm bool) error {
	cc := core.GetCacheController[string](f, "")

	if !confirm {
		keys, err := cc.IterateByPrefix(ctx, "*", 0, 100)
		approxCount := 0
		if err == nil {
			approxCount = len(keys)
		}

		if !promptConfirmScrub("cache entries", approxCount) {
			color.Yellow("Cache scrub cancelled by user")
			return fmt.Errorf("operation cancelled by user")
		}
	}

	color.HiCyan("Starting cache scrub...")

	totalDeleted := 0
	failedKeys := 0

	for {
		keys, err := cc.IterateByPrefix(ctx, "*", 0, 100)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate cache: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			if err := cc.Delete(ctx, key); err != nil {
				color.Yellow("⚠ Failed to delete cache key '%s': %v", key, err)
				failedKeys++
				continue
			}
			totalDeleted++
		}

		color.Cyan("Deleted %d cache entries...", totalDeleted)
	}

	if failedKeys > 0 {
		color.HiYellow("✓ Deleted %d cache entries (%d failed)", totalDeleted, failedKeys)
	} else {
		color.HiGreen("✓ Deleted %d total cache entries", totalDeleted)
	}
	return nil
}

func scrubBlobs(ctx context.Context, f *core.Ferry, confirm bool) error {
	bc := core.GetBlobController(f)

	if !confirm {
		keys, err := bc.IterateByPrefix(ctx, "*", 0, 100)
		approxCount := 0
		if err == nil {
			approxCount = len(keys)
		}

		if !promptConfirmScrub("blobs", approxCount) {
			color.Yellow("Blob scrub cancelled by user")
			return fmt.Errorf("operation cancelled by user")
		}
	}

	color.HiCyan("Starting blob scrub...")

	totalDeleted := 0
	failedKeys := 0

	for {
		keys, err := bc.IterateByPrefix(ctx, "*", 0, 100)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			return fmt.Errorf("failed to iterate blobs: %w", err)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			if err := bc.Delete(ctx, key); err != nil {
				color.Yellow("⚠ Failed to delete blob key '%s': %v", key, err)
				failedKeys++
				continue
			}
			totalDeleted++
		}

		color.Cyan("Deleted %d blobs...", totalDeleted)
	}

	if failedKeys > 0 {
		color.HiYellow("✓ Deleted %d blobs (%d failed)", totalDeleted, failedKeys)
	} else {
		color.HiGreen("✓ Deleted %d total blobs", totalDeleted)
	}
	return nil
}
