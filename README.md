# Ferry Quickstart

## Installation

### Linux (AMD64)
```bash
curl -L -o ferry https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-linux-amd64.alpha-rc.1-r0
chmod +x ferry
sudo mv ferry /usr/local/bin/
```

### Linux (ARM64)
```bash
curl -L -o ferry https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-linux-arm64.alpha-rc.1-r0
chmod +x ferry
sudo mv ferry /usr/local/bin/
```

### macOS (Intel)
```bash
curl -L -o ferry https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-darwin-amd64.alpha-rc.1-r0
chmod +x ferry
sudo mv ferry /usr/local/bin/
```

### macOS (Apple Silicon)
```bash
curl -L -o ferry https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-darwin-arm64.alpha-rc.1-r0
chmod +x ferry
sudo mv ferry /usr/local/bin/
```

### Windows (AMD64)
```powershell
curl -L -o ferry.exe https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-windows-amd64.alpha-rc.1-r0.exe
```

### Windows (ARM64)
```powershell
curl -L -o ferry.exe https://github.com/InsulaLabs/ferry/releases/download/alpha-rc.1-r0/ferry-windows-arm64.alpha-rc.1-r0.exe
```

## Usage

After installation, verify ferry is working:

```bash
ferry --version
```

## Configuration

### Generate Configuration

Use the `--generate` flag to create a configuration file from comma-separated endpoints:

```bash
ferry --generate red.insulalabs.io:443,blue.insulalabs.io:443,green.insulalabs.io:443
```

or 

```bash
ferry --generate api.insulalabs.io:443
```

This will output a configuration like:
```yaml
api_key_env: INSI_API_KEY
endpoints:
    - red.insulalabs.io:443
    - blue.insulalabs.io:443
    - green.insulalabs.io:443
skip_verify: false
timeout: 30s
```

### Save Configuration

Create the ferry config directory and save the generated configuration:

```bash
mkdir ~/.ferry
ferry --generate red.insulalabs.io:443,blue.insulalabs.io:443,green.insulalabs.io:443 > ~/.ferry/ferry.yaml
```

### Set Environment Variable

Set the `FERRY_CONFIG` environment variable to point to your configuration file:

```bash
export FERRY_CONFIG=~/.ferry/ferry.yaml
```

Add this to your shell profile (`.bashrc`, `.zshrc`, etc.) for persistence:

```bash
echo 'export FERRY_CONFIG=~/.ferry/ferry.yaml' >> ~/.bashrc
```

## Stress Testing with Maxx

Ferry includes a stress testing tool called `maxx` designed to slowly ramp up operations for testing rate limits and throughput. This tool performs blind testing to verify that API key limits are properly respected.

### Building Maxx

From the ferry source directory:

```bash
go build ./cmd/maxx
```

### Maxx Configuration

Generate a sample maxx configuration:

```bash
./maxx --generate red.insulalabs.io:443,blue.insulalabs.io:443 > maxx.yaml
```

This creates both the ferry configuration and stress test parameters. The generated config includes:

```yaml
api_key_env: INSI_API_KEY
endpoints:
    - red.insulalabs.io:443
    - blue.insulalabs.io:443
skip_verify: false
timeout: 30s

stress_test:
  # Load ramping parameters
  initial_concurrency: 1
  max_concurrency: 50
  ramp_up_duration: 5m
  ramp_up_steps: 10

  # Test duration
  test_duration: 10m

  # Operation parameters
  operations_per_second: 0  # 0 = unlimited

  # Key generation
  key_prefix: "maxx:stress:"
  key_range: 1000
  value_size: 100
  random_values: true

  # Reporting
  report_interval: 30s
```

### Running Stress Tests

Run a values stress test:

```bash
./maxx values
```

Run a cache stress test:

```bash
./maxx cache
```

### Understanding the Report

Maxx generates a comprehensive report showing:

- **Total Operations**: Number of operations performed
- **Success/Failure Rates**: Operation success percentages
- **Performance Metrics**: Throughput, latency percentiles
- **Rate Limiting Analysis**: Observed limits and retry patterns
- **Throughput Timeline**: Performance over time

The report is designed for blind testing - compare observed rate limits against expected server limits to verify proper enforcement.

### Key Features

- **Gradual Load Ramping**: Slowly increases concurrency to avoid overwhelming the system
- **Comprehensive Metrics**: Tracks throughput, latency, and error rates
- **Rate Limit Detection**: Parses and reports rate limiting details
- **Blind Testing**: Reports observed limits for comparison with server expectations
- **Configurable Operations**: Tests both values and cache operations
