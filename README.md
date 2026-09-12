# Prism - Advanced Proxy Router

A high-performance, feature-rich proxy routing system with dynamic node management, sticky sessions, scheduled rotation, and comprehensive management interface.

## Features

### Core Features
- **Dynamic Node Pool** - Add/remove proxy nodes without restart
- **Platform-Based Routing** - Isolated routing contexts for different services
- **Tag-Based Filtering** - Route traffic based on node tags (region, provider, etc.)
- **Power-of-Two-Choices (P2C)** - Intelligent load balancing based on latency
- **Sticky Sessions** - Maintain consistent IPs per account with configurable TTL
- **Scheduled Rotation** - Automatic IP rotation at configured intervals
- **Multiple Protocols** - Shadowsocks, VMess, Trojan, Hysteria2, TUIC, and more

### Management Features
- **Web UI** - Modern, responsive management interface
- **REST API** - Complete programmatic control
- **Backup & Restore** - Tools for state management
- **Metrics & Monitoring** - Real-time performance metrics
- **Request Logs** - Detailed connection logging

### Advanced Features
- **Secure DNS** - DNS-over-HTTPS/TLS support with failover
- **Health Checks** - Automatic node health monitoring
- **Lease Events** - Observable routing events for monitoring
- **Multiple Endpoints** - Run multiple SOCKS5 ports for different platforms

## Quick Start

### Build

```bash
go build -buildvcs=false ./cmd/prism
```

### Deploy

Deploy with management interface on port 1262:

```bash
./scripts/deploy.sh --port 1262
```

Access the management interface at: `http://localhost:1262/ui/`

### Manual Start

```bash
./prism \
  --port 1262 \
  --state-dir ./state \
  --admin-token "your-secure-token" \
  --dns-upstreams "https://1.1.1.1/dns-query,https://8.8.8.8/dns-query"
```

## Documentation

### Core Documentation
- [Architecture Overview](docs/architecture.md)
- [Configuration Guide](docs/configuration.md)
- [API Reference](docs/api.md)
- [Deployment Guide](docs/deployment.md)

### Feature Documentation
- [Sticky Sessions](docs/sticky-sessions.md)
- [Scheduled Rotation](docs/scheduled-rotation.md)
- [Platform Management](docs/platforms.md)
- [Node Pool Management](docs/node-pool.md)
- [Tag Filtering](docs/tag-filtering.md)

### Operations
- [Backup & Restore](docs/backup-restore.md)
- [Monitoring & Metrics](docs/metrics.md)
- [Troubleshooting](docs/troubleshooting.md)

## Usage Examples

### Create a Platform

```bash
curl -X POST http://localhost:1262/api/v1/platforms \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "production",
    "name": "Production Platform",
    "sticky_enabled": true,
    "sticky_ttl": "24h",
    "scheduled_rotation_enabled": true,
    "scheduled_rotation_interval": "6h"
  }'
```

### Add a Proxy Node

```bash
curl -X POST http://localhost:1262/api/v1/nodes \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "node": {
      "type": "shadowsocks",
      "server": "1.2.3.4",
      "server_port": 8388,
      "method": "aes-256-gcm",
      "password": "secure-password"
    },
    "tags": {
      "region": "us-west",
      "provider": "aws",
      "tier": "premium"
    }
  }'
```

### Create an Endpoint

```bash
curl -X POST http://localhost:1262/api/v1/endpoints \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "prod-socks",
    "port": 10808,
    "platform_id": "production",
    "enabled": true
  }'
```

### Connect Through SOCKS5

```bash
curl -x socks5h://user@localhost:10808 https://api.ipify.org
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Management Layer                        │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │   Web UI    │  │   REST API   │  │   Admin Token    │   │
│  └─────────────┘  └──────────────┘  └──────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────┐
│                      Control Plane                           │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │  Platforms  │  │  Endpoints   │  │   Node Pool      │   │
│  └─────────────┘  └──────────────┘  └──────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────┐
│                      Routing Layer                           │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │   Router    │  │  P2C Select  │  │  Sticky Leases   │   │
│  └─────────────┘  └──────────────┘  └──────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────┐
│                      Data Plane                              │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │SOCKS5 Server│  │ Outbound Mgr │  │  DNS Resolver    │   │
│  └─────────────┘  └──────────────┘  └──────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                            │
                    ┌───────┴────────┐
                    │  Proxy Nodes   │
                    │  (SS/VMess/etc)│
                    └────────────────┘
```

## Key Components

### Router
- Maintains per-platform routing state
- Implements P2C load balancing
- Manages sticky session leases
- Emits routing events

### Scheduled Rotator
- Background task for automatic IP rotation
- Configurable per-platform intervals
- Age-based lease deletion
- Parallel platform processing

### Node Pool
- Centralized proxy node management
- Tag-based organization
- Health monitoring integration
- Dynamic updates without restart

### Endpoint Manager
- Multiple SOCKS5 listeners
- Platform isolation
- Authentication via account ID
- Graceful shutdown

## Configuration

### Command Line Flags

```
--port <port>                Management interface port (default: 8080)
--state-dir <path>          State directory for persistence
--admin-token <token>       Admin API authentication token
--log-level <level>         Log level: debug, info, warn, error
--dns-upstreams <urls>      Comma-separated DNS upstream URLs
```

### Environment Variables

```bash
PORT=1262
STATE_DIR=/opt/prism/state
ADMIN_TOKEN=your-secure-token
LOG_LEVEL=info
DNS_UPSTREAMS=https://1.1.1.1/dns-query,https://8.8.8.8/dns-query
```

## Management Interface

The Web UI provides:

- **Dashboard** - System overview and statistics
- **Platforms** - Create and configure routing platforms
- **Endpoints** - Manage SOCKS5 listeners
- **Leases** - View active sticky sessions
- **Metrics** - Performance monitoring
- **Logs** - Request logging and debugging

## API Reference

Full API documentation: [docs/api.md](docs/api.md)

Key endpoints:

- `GET /api/v1/system/info` - System information
- `GET /api/v1/platforms` - List platforms
- `POST /api/v1/platforms` - Create platform
- `PATCH /api/v1/platforms/{id}` - Update platform
- `GET /api/v1/endpoints` - List endpoints
- `POST /api/v1/endpoints` - Create endpoint
- `GET /api/v1/nodes` - List nodes
- `POST /api/v1/nodes` - Add node
- `GET /api/v1/platforms/{id}/leases` - List leases
- `DELETE /api/v1/platforms/{id}/leases/{account}` - Delete lease

## Backup & Restore

### Create Backup

```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -c
```

### Restore Backup

```bash
./scripts/prism-backup.sh restore -b ./backups/backup-latest.tar.gz -d ./state
```

### List Backups

```bash
./scripts/prism-backup.sh list -o ./backups
```

See [Backup & Restore Guide](docs/backup-restore.md) for details.

## Development

### Prerequisites

- Go 1.22 or later
- Linux/macOS (Windows untested)

### Build from Source

```bash
git clone https://github.com/yourusername/Prism.git
cd Prism
go build -buildvcs=false ./cmd/prism
```

### Run Tests

```bash
# All tests
go test ./...

# Specific package
go test ./internal/routing -v

# Scheduled rotator tests
go test ./internal/routing -run TestScheduledRotator -v
```

### Project Structure

```
Prism/
├── cmd/prism/              # Main entry point
├── internal/
│   ├── api/               # REST API and Web UI
│   ├── routing/           # Router and lease management
│   ├── platform/          # Platform configuration
│   ├── node/              # Node pool management
│   ├── endpoint/          # SOCKS5 endpoint manager
│   ├── outbound/          # Outbound connection builder
│   ├── service/           # Control plane services
│   └── metrics/           # Metrics collection
├── docs/                  # Documentation
├── scripts/               # Deployment and backup scripts
└── state/                 # Runtime state (created on first run)
```

## Monitoring

### Health Check

```bash
curl http://localhost:1262/health
```

Response: `200 OK` if healthy

### Metrics

```bash
curl -H "Authorization: Bearer YOUR_TOKEN" \
  http://localhost:1262/api/v1/metrics/snapshots/summary
```

### Logs

```bash
# If running as systemd service
sudo journalctl -u prism -f

# If running manually
./prism --log-level debug
```

## Performance

Prism is designed for high performance:

- **Low Latency** - P2C selection minimizes connection latency
- **Efficient** - Event-driven architecture, minimal overhead
- **Scalable** - Supports thousands of concurrent connections
- **Fast Routing** - Lock-free reads, optimized data structures

Typical performance:
- Routing decision: < 1ms
- Lease lookup: < 100μs
- Node selection: < 500μs

## Security

### Authentication

All API endpoints require Bearer token authentication:

```bash
Authorization: Bearer YOUR_ADMIN_TOKEN
```

### Best Practices

1. **Use strong admin tokens** (64+ random characters)
2. **Enable TLS** with reverse proxy (nginx, caddy)
3. **Restrict network access** with firewall rules
4. **Rotate tokens regularly**
5. **Monitor access logs**
6. **Keep backups encrypted**

See [Security Guide](docs/security.md) for details.

## License

[Your License Here]

## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Submit a pull request

## Support

- Documentation: [docs/](docs/)
- Issues: GitHub Issues
- Discussions: GitHub Discussions

## Acknowledgments

Built with:
- [sing-box](https://github.com/sagernet/sing-box) - Protocol implementations
- Go standard library - Core functionality
