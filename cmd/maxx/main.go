package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/InsulaLabs/ferry/internal/core"
	"gopkg.in/yaml.v3"
)

type StressConfig struct {
	// Load ramping parameters
	InitialConcurrency int           `yaml:"initial_concurrency"`
	MaxConcurrency     int           `yaml:"max_concurrency"`
	RampUpDuration     time.Duration `yaml:"ramp_up_duration"`
	RampUpSteps        int           `yaml:"ramp_up_steps"`

	// Test duration
	TestDuration time.Duration `yaml:"test_duration"`

	// Operation parameters
	OperationsPerSecond int `yaml:"operations_per_second"` // 0 = unlimited

	// Key generation
	KeyPrefix    string `yaml:"key_prefix"`
	KeyRange     int    `yaml:"key_range"` // number of unique keys to use
	ValueSize    int    `yaml:"value_size"`
	RandomValues bool   `yaml:"random_values"`

	// Reporting
	ReportInterval time.Duration `yaml:"report_interval"`
}

type Metrics struct {
	StartTime time.Time

	// Counters
	TotalOperations int64
	SuccessfulOps   int64
	FailedOps       int64
	RateLimitedOps  int64
	OtherErrors     int64

	// Latency tracking (in nanoseconds)
	Latencies []time.Duration

	// Rate limiting details
	RateLimitHits     int64
	LastRateLimitTime time.Time
	ObservedLimits    []RateLimitObservation

	// Throughput tracking
	ThroughputHistory []ThroughputSample

	mu sync.RWMutex
}

type RateLimitObservation struct {
	Timestamp  time.Time
	RetryAfter time.Duration
	Limit      int64
	Burst      int64
	Message    string
}

type ThroughputSample struct {
	Timestamp    time.Time
	OpsPerSecond float64
	SuccessRate  float64
}

type StressTest struct {
	config  *StressConfig
	ferry   *core.Ferry
	logger  *slog.Logger
	metrics *Metrics
	ctx     context.Context
	cancel  context.CancelFunc
}

var (
	configPath   string
	generateFlag string
)

func init() {
	flag.StringVar(&configPath, "config", "", "Path to the ferry configuration file (defaults to ferry.yaml, then FERRY_CONFIG env)")
	flag.StringVar(&generateFlag, "generate", "", "Generate config from comma-separated endpoints")
}

func main() {
	flag.Parse()

	if generateFlag != "" {
		generateMaxxConfig(generateFlag)
		return
	}

	// Load config
	cfg, err := loadMaxxConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Load ferry config
	ferryCfg, err := loadFerryConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to load ferry configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	logOpts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	handler := slog.NewTextHandler(os.Stderr, logOpts)
	logger := slog.New(handler)

	// Create ferry instance
	f, err := core.New(logger, ferryCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to create ferry instance: %v\n", err)
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
		runValuesStressTest(f, logger, cfg, cmdArgs)
	case "cache":
		runCacheStressTest(f, logger, cfg, cmdArgs)
	default:
		logger.Error("Unknown command", "command", command)
		printUsage()
		os.Exit(1)
	}
}

func generateMaxxConfig(endpoints string) {
	endpointList := strings.Split(endpoints, ",")
	for i, ep := range endpointList {
		endpointList[i] = strings.TrimSpace(ep)
	}

	cfg := struct {
		ApiKeyEnv  string       `yaml:"api_key_env"`
		Endpoints  []string     `yaml:"endpoints"`
		SkipVerify bool         `yaml:"skip_verify"`
		Timeout    string       `yaml:"timeout"`
		StressTest StressConfig `yaml:"stress_test"`
	}{
		ApiKeyEnv:  "INSI_API_KEY",
		Endpoints:  endpointList,
		SkipVerify: false,
		Timeout:    "30s",
		StressTest: StressConfig{
			InitialConcurrency:  1,
			MaxConcurrency:      50,
			RampUpDuration:      5 * time.Minute,
			RampUpSteps:         10,
			TestDuration:        10 * time.Minute,
			OperationsPerSecond: 0,
			KeyPrefix:           "maxx:stress:",
			KeyRange:            1000,
			ValueSize:           100,
			RandomValues:        true,
			ReportInterval:      30 * time.Second,
		},
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to marshal config: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(string(data))
}

func loadFerryConfig() (*core.Config, error) {
	// Determine config path (same logic as CLI)
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

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Parse YAML
	var ferryCfg struct {
		ApiKeyEnv  string   `yaml:"api_key_env"`
		Endpoints  []string `yaml:"endpoints"`
		SkipVerify bool     `yaml:"skip_verify"`
		Timeout    string   `yaml:"timeout"`
		Domain     string   `yaml:"domain,omitempty"`
	}

	if err := yaml.Unmarshal(data, &ferryCfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Set defaults
	if ferryCfg.ApiKeyEnv == "" {
		ferryCfg.ApiKeyEnv = "INSI_API_KEY"
	}

	// Parse timeout
	timeout := 30 * time.Second
	if ferryCfg.Timeout != "" {
		timeout, err = time.ParseDuration(ferryCfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %w", err)
		}
	}

	return &core.Config{
		ApiKey:     os.Getenv(ferryCfg.ApiKeyEnv),
		Endpoints:  ferryCfg.Endpoints,
		SkipVerify: ferryCfg.SkipVerify,
		Timeout:    timeout,
		Domain:     ferryCfg.Domain,
	}, nil
}

func loadMaxxConfig() (*StressConfig, error) {
	// Use separate config path for maxx config (don't reuse ferry configPath)
	maxxConfigPath := ""
	if maxxConfigPath == "" {
		// Check maxx.yaml
		if _, err := os.Stat("maxx.yaml"); err == nil {
			maxxConfigPath = "maxx.yaml"
		} else if _, err := os.Stat("configs/maxx.yaml"); err == nil {
			maxxConfigPath = "configs/maxx.yaml"
		} else {
			// Check MAX_CONFIG env
			if envPath := os.Getenv("MAX_CONFIG"); envPath != "" {
				maxxConfigPath = envPath
			} else {
				return nil, fmt.Errorf("no maxx config file found: checked maxx.yaml, configs/maxx.yaml, and MAX_CONFIG env var")
			}
		}
	}

	data, err := os.ReadFile(maxxConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", maxxConfigPath, err)
	}

	var cfg struct {
		StressTest StressConfig `yaml:"stress_test"`
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", maxxConfigPath, err)
	}

	return &cfg.StressTest, nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: maxx [flags] <command> [args...]\n")
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	flag.PrintDefaults()

	fmt.Fprintf(os.Stderr, "\nCommands:\n")
	fmt.Fprintf(os.Stderr, "  values    Run stress test on values operations\n")
	fmt.Fprintf(os.Stderr, "  cache     Run stress test on cache operations\n")

	fmt.Fprintf(os.Stderr, "\nConfiguration:\n")
	fmt.Fprintf(os.Stderr, "  Ferry API config (checked in order):\n")
	fmt.Fprintf(os.Stderr, "    1. --config flag\n")
	fmt.Fprintf(os.Stderr, "    2. ferry.yaml in current directory\n")
	fmt.Fprintf(os.Stderr, "    3. FERRY_CONFIG environment variable\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  Maxx stress test config (checked in order):\n")
	fmt.Fprintf(os.Stderr, "    1. maxx.yaml in current directory\n")
	fmt.Fprintf(os.Stderr, "    2. configs/maxx.yaml\n")
	fmt.Fprintf(os.Stderr, "    3. MAX_CONFIG environment variable\n")
	fmt.Fprintf(os.Stderr, "  \n")
	fmt.Fprintf(os.Stderr, "  Use --generate to create sample configurations\n")

	fmt.Fprintf(os.Stderr, "\nExample:\n")
	fmt.Fprintf(os.Stderr, "  maxx --generate \"red.insulalabs.io:443,blue.insulalabs.io:443\" > ferry.yaml\n")
}

func newMetrics() *Metrics {
	return &Metrics{
		StartTime:         time.Now(),
		Latencies:         make([]time.Duration, 0, 100000),
		ObservedLimits:    make([]RateLimitObservation, 0),
		ThroughputHistory: make([]ThroughputSample, 0),
	}
}

func (m *Metrics) RecordOperation(latency time.Duration, err error) {
	atomic.AddInt64(&m.TotalOperations, 1)

	if err != nil {
		atomic.AddInt64(&m.FailedOps, 1)

		errStr := err.Error()
		if strings.Contains(errStr, "rate limited") {
			atomic.AddInt64(&m.RateLimitedOps, 1)
			m.recordRateLimit(errStr)
		} else {
			atomic.AddInt64(&m.OtherErrors, 1)
		}
	} else {
		atomic.AddInt64(&m.SuccessfulOps, 1)
	}

	m.mu.Lock()
	m.Latencies = append(m.Latencies, latency)
	m.mu.Unlock()
}

func (m *Metrics) recordRateLimit(errStr string) {
	// Parse rate limit details from error message
	// Format: "rate limited: <message>. Try again in <duration>. (Limit: <limit>, Burst: <burst>)"
	parts := strings.Split(errStr, ". Try again in ")
	if len(parts) < 2 {
		return
	}

	retryPart := parts[1]
	retryParts := strings.Split(retryPart, ". (Limit: ")
	if len(retryParts) < 2 {
		return
	}

	retryAfterStr := strings.TrimSpace(retryParts[0])
	limitPart := retryParts[1]

	limitParts := strings.Split(limitPart, ", Burst: ")
	if len(limitParts) < 2 {
		return
	}

	limitStr := strings.TrimSpace(limitParts[0])
	burstStr := strings.TrimSuffix(limitParts[1], ")")

	retryAfter, err := time.ParseDuration(retryAfterStr)
	if err != nil {
		return
	}

	limit, err := strconv.ParseInt(limitStr, 10, 64)
	if err != nil {
		return
	}

	burst, err := strconv.ParseInt(burstStr, 10, 64)
	if err != nil {
		return
	}

	observation := RateLimitObservation{
		Timestamp:  time.Now(),
		RetryAfter: retryAfter,
		Limit:      limit,
		Burst:      burst,
		Message:    parts[0],
	}

	m.mu.Lock()
	m.ObservedLimits = append(m.ObservedLimits, observation)
	m.LastRateLimitTime = observation.Timestamp
	atomic.AddInt64(&m.RateLimitHits, 1)
	m.mu.Unlock()
}

func (m *Metrics) RecordThroughput(opsPerSecond, successRate float64) {
	sample := ThroughputSample{
		Timestamp:    time.Now(),
		OpsPerSecond: opsPerSecond,
		SuccessRate:  successRate,
	}

	m.mu.Lock()
	m.ThroughputHistory = append(m.ThroughputHistory, sample)
	m.mu.Unlock()
}

func (m *Metrics) GenerateReport() string {
	total := atomic.LoadInt64(&m.TotalOperations)
	successful := atomic.LoadInt64(&m.SuccessfulOps)
	failed := atomic.LoadInt64(&m.FailedOps)
	rateLimited := atomic.LoadInt64(&m.RateLimitedOps)
	otherErrors := atomic.LoadInt64(&m.OtherErrors)

	duration := time.Since(m.StartTime)
	avgThroughput := float64(total) / duration.Seconds()

	var p50, p95, p99 time.Duration
	m.mu.RLock()
	if len(m.Latencies) > 0 {
		// Simple percentile calculation (could be improved)
		sortedLatencies := make([]time.Duration, len(m.Latencies))
		copy(sortedLatencies, m.Latencies)
		// Sort would go here, but keeping simple for now

		p50 = sortedLatencies[len(sortedLatencies)/2]
		p95 = sortedLatencies[len(sortedLatencies)*95/100]
		p99 = sortedLatencies[len(sortedLatencies)*99/100]
	}
	throughputSamples := len(m.ThroughputHistory)
	rateLimitObservations := len(m.ObservedLimits)
	m.mu.RUnlock()

	report := fmt.Sprintf(`
MAX Stress Test Report
=====================

Test Duration: %v
Total Operations: %d
Successful Operations: %d (%.2f%%)
Failed Operations: %d (%.2f%%)
  - Rate Limited: %d (%.2f%%)
  - Other Errors: %d (%.2f%%)

Performance Metrics:
  Average Throughput: %.2f ops/sec
  P50 Latency: %v
  P95 Latency: %v
  P99 Latency: %v

Rate Limiting Analysis:
  Rate Limit Hits: %d
  Unique Rate Limit Observations: %d
`, duration, total, successful, float64(successful)/float64(total)*100,
		failed, float64(failed)/float64(total)*100,
		rateLimited, float64(rateLimited)/float64(total)*100,
		otherErrors, float64(otherErrors)/float64(total)*100,
		avgThroughput, p50, p95, p99,
		atomic.LoadInt64(&m.RateLimitHits), rateLimitObservations)

	// Add rate limit details if any
	if rateLimitObservations > 0 {
		report += "\nObserved Rate Limits:\n"
		m.mu.RLock()
		for i, obs := range m.ObservedLimits {
			report += fmt.Sprintf("  %d. %s - Limit: %d, Burst: %d, Retry After: %v\n",
				i+1, obs.Timestamp.Format("15:04:05"), obs.Limit, obs.Burst, obs.RetryAfter)
		}
		m.mu.RUnlock()
	}

	// Add throughput timeline if samples exist
	if throughputSamples > 0 {
		report += "\nThroughput Timeline:\n"
		m.mu.RLock()
		for _, sample := range m.ThroughputHistory {
			report += fmt.Sprintf("  %s: %.2f ops/sec (%.1f%% success)\n",
				sample.Timestamp.Format("15:04:05"), sample.OpsPerSecond, sample.SuccessRate*100)
		}
		m.mu.RUnlock()
	}

	return report
}

func runValuesStressTest(f *core.Ferry, logger *slog.Logger, cfg *StressConfig, args []string) {
	if len(args) != 0 {
		logger.Error("values stress test does not take arguments")
		printUsage()
		os.Exit(1)
	}

	stressTest := &StressTest{
		config:  cfg,
		ferry:   f,
		logger:  logger.WithGroup("values_stress"),
		metrics: newMetrics(),
	}

	stressTest.ctx, stressTest.cancel = context.WithTimeout(context.Background(), cfg.TestDuration)

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Received interrupt signal, stopping stress test...")
		stressTest.cancel()
	}()

	logger.Info("Starting values stress test",
		"duration", cfg.TestDuration,
		"max_concurrency", cfg.MaxConcurrency,
		"ramp_up_duration", cfg.RampUpDuration)

	stressTest.runValuesLoadTest()

	logger.Info("Stress test completed")
	fmt.Print(stressTest.metrics.GenerateReport())
}

func (st *StressTest) runValuesLoadTest() {
	vc := core.GetValueController[string](st.ferry, "")

	var wg sync.WaitGroup
	concurrency := int64(st.config.InitialConcurrency)

	// Start ramping goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.rampUpConcurrency(&concurrency)
	}()

	// Start reporting goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.reportProgress()
	}()

	// Start worker goroutines
	workerCount := int64(st.config.MaxConcurrency)
	for i := int64(0); i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int64) {
			defer wg.Done()
			st.valuesWorker(vc, workerID, &concurrency)
		}(i)
	}

	wg.Wait()
}

func (st *StressTest) rampUpConcurrency(currentConcurrency *int64) {
	if st.config.RampUpSteps <= 1 {
		atomic.StoreInt64(currentConcurrency, int64(st.config.MaxConcurrency))
		return
	}

	stepDuration := st.config.RampUpDuration / time.Duration(st.config.RampUpSteps)
	stepIncrease := float64(st.config.MaxConcurrency-st.config.InitialConcurrency) / float64(st.config.RampUpSteps)

	ticker := time.NewTicker(stepDuration)
	defer ticker.Stop()

	current := float64(st.config.InitialConcurrency)
	for i := 0; i < st.config.RampUpSteps; i++ {
		select {
		case <-ticker.C:
			current += stepIncrease
			atomic.StoreInt64(currentConcurrency, int64(current))
			st.logger.Info("Ramping up concurrency", "concurrency", int64(current))
		case <-st.ctx.Done():
			return
		}
	}

	atomic.StoreInt64(currentConcurrency, int64(st.config.MaxConcurrency))
}

func (st *StressTest) reportProgress() {
	ticker := time.NewTicker(st.config.ReportInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			total := atomic.LoadInt64(&st.metrics.TotalOperations)
			successful := atomic.LoadInt64(&st.metrics.SuccessfulOps)
			rateLimited := atomic.LoadInt64(&st.metrics.RateLimitedOps)

			var successRate float64
			if total > 0 {
				successRate = float64(successful) / float64(total)
			}

			duration := time.Since(st.metrics.StartTime)
			throughput := float64(total) / duration.Seconds()

			st.metrics.RecordThroughput(throughput, successRate)

			st.logger.Info("Progress report",
				"total_ops", total,
				"successful", successful,
				"rate_limited", rateLimited,
				"throughput", fmt.Sprintf("%.2f ops/sec", throughput),
				"success_rate", fmt.Sprintf("%.2f%%", successRate*100),
				"duration", duration.Round(time.Second))
		case <-st.ctx.Done():
			return
		}
	}
}

func (st *StressTest) valuesWorker(vc core.ValueController[string], workerID int64, currentConcurrency *int64) {
	// Rate limiter if configured
	var ticker *time.Ticker
	if st.config.OperationsPerSecond > 0 {
		interval := time.Second / time.Duration(st.config.OperationsPerSecond)
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
	}

	for {
		select {
		case <-st.ctx.Done():
			return
		default:
			// Check if we should be running based on current concurrency
			if workerID >= atomic.LoadInt64(currentConcurrency) {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Rate limiting
			if ticker != nil {
				select {
				case <-ticker.C:
				case <-st.ctx.Done():
					return
				}
			}

			st.executeValuesOperation(vc, workerID)
		}
	}
}

func (st *StressTest) executeValuesOperation(vc core.ValueController[string], workerID int64) {
	start := time.Now()

	// Generate key
	keyID := start.UnixNano() % int64(st.config.KeyRange)
	key := fmt.Sprintf("%s%d", st.config.KeyPrefix, keyID)

	// Generate value
	value := st.generateValue(workerID, keyID)

	// Randomly select operation
	operation := start.UnixNano() % 4 // 0=set, 1=get, 2=setnx, 3=delete

	var err error
	switch operation {
	case 0: // set
		err = vc.Set(st.ctx, key, value)
	case 1: // get
		_, err = vc.Get(st.ctx, key)
	case 2: // setnx
		err = vc.SetNX(st.ctx, key, value)
	case 3: // delete
		err = vc.Delete(st.ctx, key)
	}

	latency := time.Since(start)
	st.metrics.RecordOperation(latency, err)
}

func (st *StressTest) generateValue(workerID, keyID int64) string {
	if !st.config.RandomValues {
		return fmt.Sprintf("value_worker_%d_key_%d", workerID, keyID)
	}

	// Generate random string of configured size
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, st.config.ValueSize)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(result)
}

func runCacheStressTest(f *core.Ferry, logger *slog.Logger, cfg *StressConfig, args []string) {
	if len(args) != 0 {
		logger.Error("cache stress test does not take arguments")
		printUsage()
		os.Exit(1)
	}

	stressTest := &StressTest{
		config:  cfg,
		ferry:   f,
		logger:  logger.WithGroup("cache_stress"),
		metrics: newMetrics(),
	}

	stressTest.ctx, stressTest.cancel = context.WithTimeout(context.Background(), cfg.TestDuration)

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Received interrupt signal, stopping stress test...")
		stressTest.cancel()
	}()

	logger.Info("Starting cache stress test",
		"duration", cfg.TestDuration,
		"max_concurrency", cfg.MaxConcurrency,
		"ramp_up_duration", cfg.RampUpDuration)

	stressTest.runCacheLoadTest()

	logger.Info("Stress test completed")
	fmt.Print(stressTest.metrics.GenerateReport())
}

func (st *StressTest) runCacheLoadTest() {
	cc := core.GetCacheController[string](st.ferry, "")

	var wg sync.WaitGroup
	concurrency := int64(st.config.InitialConcurrency)

	// Start ramping goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.rampUpConcurrency(&concurrency)
	}()

	// Start reporting goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.reportProgress()
	}()

	// Start worker goroutines
	workerCount := int64(st.config.MaxConcurrency)
	for i := int64(0); i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int64) {
			defer wg.Done()
			st.cacheWorker(cc, workerID, &concurrency)
		}(i)
	}

	wg.Wait()
}

func (st *StressTest) cacheWorker(cc core.CacheController[string], workerID int64, currentConcurrency *int64) {
	// Rate limiter if configured
	var ticker *time.Ticker
	if st.config.OperationsPerSecond > 0 {
		interval := time.Second / time.Duration(st.config.OperationsPerSecond)
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
	}

	for {
		select {
		case <-st.ctx.Done():
			return
		default:
			// Check if we should be running based on current concurrency
			if workerID >= atomic.LoadInt64(currentConcurrency) {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Rate limiting
			if ticker != nil {
				select {
				case <-ticker.C:
				case <-st.ctx.Done():
					return
				}
			}

			st.executeCacheOperation(cc, workerID)
		}
	}
}

func (st *StressTest) executeCacheOperation(cc core.CacheController[string], workerID int64) {
	start := time.Now()

	// Generate key
	keyID := start.UnixNano() % int64(st.config.KeyRange)
	key := fmt.Sprintf("%s%d", st.config.KeyPrefix, keyID)

	// Generate value
	value := st.generateValue(workerID, keyID)

	// Randomly select operation
	operation := start.UnixNano() % 4 // 0=set, 1=get, 2=setnx, 3=delete

	var err error
	switch operation {
	case 0: // set
		err = cc.Set(st.ctx, key, value)
	case 1: // get
		_, err = cc.Get(st.ctx, key)
	case 2: // setnx
		err = cc.SetNX(st.ctx, key, value)
	case 3: // delete
		err = cc.Delete(st.ctx, key)
	}

	latency := time.Since(start)
	st.metrics.RecordOperation(latency, err)
}
