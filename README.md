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
