# Ferry

Client tools for the Insula Labs platform.

## Binaries

- `ferry` - CLI for data operations
- `portal` - P2P HTTP proxy using WebRTC

## Configuration

Create a `ferry.yaml` file or set `FERRY_CONFIG` environment variable:

```yaml
api_key_env: INSI_API_KEY
endpoints:
  - api.insulalabs.io:443
skip_verify: false
timeout: 30s
```

Generate config from endpoints:

```bash
ferry --generate "endpoint1:443,endpoint2:443" > ferry.yaml
```

Set API key via environment variable (default: `INSI_API_KEY`).

## Ferry CLI

### Commands

**Connectivity**
```bash
ferry ping
```

**Value Store** (persistent)
```bash
ferry values get <key>
ferry values set <key> <value>
ferry values setnx <key> <value>
ferry values delete <key>
ferry values cas <key> <old_value> <new_value>
ferry values bump <key> <increment>
ferry values iterate <prefix> [offset] [limit]
```

**Cache** (volatile)
```bash
ferry cache get <key>
ferry cache set <key> <value>
ferry cache setnx <key> <value>
ferry cache delete <key>
ferry cache cas <key> <old_value> <new_value>
ferry cache iterate <prefix> [offset] [limit]
```

**Events** (pub/sub)
```bash
ferry events publish <topic> <data>
ferry events subscribe <topic>
ferry events purge
```

**Blob Storage**
```bash
ferry blob upload <key> <file>
ferry blob download <key> [output_file]
ferry blob delete <key>
ferry blob iterate <prefix> [offset] [limit]
```

**P2P File Transfer**
```bash
ferry p2p receive <session-id> <output-file>
ferry p2p send <session-id> <file>
```

## Portal

WebRTC-based P2P HTTP proxy.

**Receiver** (waits for connection, proxies to local server):
```bash
portal receiver <session-id> <local-addr>
```

**Sender** (connects to receiver, proxies from local server):
```bash
portal sender <session-id> <local-addr>
```

Signaling uses cache for coordination.

## Build

```bash
make prod        # Build for current platform
make cross       # Cross-compile for all platforms
make clean       # Remove build artifacts
```

Outputs to `build/` directory.

## Requirements

- Go 1.24.2+
- API key for Insula Labs platform

