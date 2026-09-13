# Prism

[English](#english) | [中文](#中文)

---

## English

A high-performance proxy router with intelligent node management, quality monitoring, and scheduled rotation capabilities.

### Features

- **Multi-Protocol Support**: Shadowsocks, VMess, Trojan, WireGuard, and more
- **Intelligent Routing**: Platform-based routing with sticky sessions and load balancing
- **Quality Monitoring**: Real-time node quality assessment and egress IP verification
- **Scheduled Rotation**: Automatic node rotation with configurable intervals
- **Management Interface**: RESTful API and web-based UI for easy configuration
- **Metrics & Analytics**: Built-in metrics collection and historical data analysis
- **High Performance**: Efficient connection handling with minimal overhead
- **Secure by Default**: Token-based authentication and encrypted communications

### Quick Start

#### Prerequisites

- Go 1.21 or later
- Linux system with systemd (recommended)
- Root or sudo access for service installation

#### Installation

1. Clone the repository:
```bash
git clone https://github.com/ermitcc/Prism.git
cd Prism
```

2. Build the binary:
```bash
go build -buildvcs=false ./cmd/prism
```

3. Deploy with default configuration:
```bash
./scripts/deploy.sh
```

4. Access the web interface:
```
http://localhost:8080/ui/
```

Use the admin token displayed after deployment or found in `state/.admin_token`.

#### Custom Deployment

Deploy with custom port and state directory:
```bash
./scripts/deploy.sh --port 1080 --state-dir /opt/prism/state
```

### Architecture

Prism uses a modular architecture:

- **Control Plane**: Management API and web interface
- **Data Plane**: High-performance proxy routing engine
- **Node Pool**: Dynamic node management with health checks
- **Quality System**: Real-time IP quality assessment
- **Metrics Engine**: Performance monitoring and analytics

### Configuration

#### Environment Variables

```bash
PORT=8080                    # Management interface port
STATE_DIR=./state            # State and database directory
LOG_LEVEL=info              # Logging level (debug, info, warn, error)
ADMIN_TOKEN=your-token      # Admin authentication token
DNS_UPSTREAMS=https://1.1.1.1/dns-query  # DNS-over-HTTPS upstreams
```

#### Platform Setup

Create a platform via API:
```bash
curl -X POST http://localhost:8080/api/v1/platforms \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-platform",
    "name": "My Platform",
    "sticky_enabled": true,
    "scheduled_rotation_enabled": true,
    "scheduled_rotation_interval": "1h"
  }'
```

Add nodes to the pool:
```bash
curl -X POST http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "node": {
      "type": "shadowsocks",
      "server": "1.2.3.4",
      "server_port": 8388,
      "method": "aes-256-gcm",
      "password": "your-password"
    },
    "tags": {
      "region": "us-west"
    }
  }'
```

Create an endpoint:
```bash
curl -X POST http://localhost:8080/api/v1/endpoints \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-endpoint",
    "port": 8081,
    "platform_id": "my-platform",
    "enabled": true
  }'
```

### Management

#### Service Control

```bash
# Start service
sudo systemctl start prism

# Stop service
sudo systemctl stop prism

# Restart service
sudo systemctl restart prism

# Check status
sudo systemctl status prism

# View logs
sudo journalctl -u prism -f
```

#### Backup and Restore

Create backup:
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -c
```

Restore from backup:
```bash
./scripts/prism-backup.sh restore -d ./state -f ./backups/backup-20260912.tar.gz
```

### API Documentation

Full API documentation is available at `/ui/docs` when Prism is running.

Key endpoints:
- `GET /api/v1/platforms` - List all platforms
- `POST /api/v1/nodes` - Add node to pool
- `GET /api/v1/endpoints` - List endpoints
- `GET /api/v1/metrics/snapshots/summary` - Get metrics summary
- `POST /api/v1/quality/probe` - Probe node quality

### Development

#### Project Structure

```
Prism/
├── cmd/prism/          # Main application entry point
├── internal/           # Internal packages
│   ├── api/           # REST API handlers
│   ├── config/        # Configuration management
│   ├── node/          # Node pool and management
│   ├── outbound/      # Proxy protocol implementations
│   ├── platform/      # Platform logic
│   ├── quality/       # Quality monitoring system
│   ├── rotation/      # Scheduled rotation
│   └── service/       # Control plane service
├── web/               # Web UI (embedded)
├── scripts/           # Deployment and utility scripts
└── docs/              # Documentation
```

#### Building from Source

```bash
# Build
go build -buildvcs=false ./cmd/prism

# Run tests (requires test files)
go test ./...

# Build with optimizations
go build -ldflags="-s -w" -buildvcs=false ./cmd/prism
```

### Security

- All API endpoints require token authentication
- Tokens are generated securely and stored with restricted permissions
- State database contains sensitive configuration - protect access
- Use reverse proxy with TLS for production deployments
- Regular security updates recommended

### Performance

Prism is designed for high performance:
- Handles 10,000+ concurrent connections per endpoint
- Sub-millisecond routing decisions
- Minimal memory footprint (~50MB base)
- Efficient connection pooling and reuse

### License

MIT License - see LICENSE file for details

### Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Submit a pull request

### Support

- GitHub Issues: https://github.com/ermitcc/Prism/issues
- Documentation: https://github.com/ermitcc/Prism/tree/main/docs

---

## 中文

高性能代理路由器，具备智能节点管理、质量监控和定时轮换功能。

### 功能特性

- **多协议支持**：支持 Shadowsocks、VMess、Trojan、WireGuard 等
- **智能路由**：基于平台的路由，支持会话保持和负载均衡
- **质量监控**：实时节点质量评估和出口 IP 验证
- **定时轮换**：可配置间隔的自动节点轮换
- **管理界面**：RESTful API 和基于 Web 的 UI，便于配置
- **指标分析**：内置指标采集和历史数据分析
- **高性能**：高效的连接处理，开销最小
- **默认安全**：基于 Token 的认证和加密通信

### 快速开始

#### 系统要求

- Go 1.21 或更高版本
- Linux 系统（推荐使用 systemd）
- Root 或 sudo 权限（用于安装服务）

#### 安装步骤

1. 克隆仓库：
```bash
git clone https://github.com/ermitcc/Prism.git
cd Prism
```

2. 构建二进制文件：
```bash
go build -buildvcs=false ./cmd/prism
```

3. 使用默认配置部署：
```bash
./scripts/deploy.sh
```

4. 访问 Web 界面：
```
http://localhost:8080/ui/
```

使用部署后显示的管理员 token，或在 `state/.admin_token` 文件中查找。

#### 自定义部署

使用自定义端口和状态目录部署：
```bash
./scripts/deploy.sh --port 1080 --state-dir /opt/prism/state
```

### 架构设计

Prism 采用模块化架构：

- **控制平面**：管理 API 和 Web 界面
- **数据平面**：高性能代理路由引擎
- **节点池**：动态节点管理和健康检查
- **质量系统**：实时 IP 质量评估
- **指标引擎**：性能监控和分析

### 配置说明

#### 环境变量

```bash
PORT=8080                    # 管理界面端口
STATE_DIR=./state            # 状态和数据库目录
LOG_LEVEL=info              # 日志级别（debug, info, warn, error）
ADMIN_TOKEN=your-token      # 管理员认证 token
DNS_UPSTREAMS=https://1.1.1.1/dns-query  # DNS-over-HTTPS 上游
```

#### 平台配置

通过 API 创建平台：
```bash
curl -X POST http://localhost:8080/api/v1/platforms \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-platform",
    "name": "My Platform",
    "sticky_enabled": true,
    "scheduled_rotation_enabled": true,
    "scheduled_rotation_interval": "1h"
  }'
```

添加节点到节点池：
```bash
curl -X POST http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "node": {
      "type": "shadowsocks",
      "server": "1.2.3.4",
      "server_port": 8388,
      "method": "aes-256-gcm",
      "password": "your-password"
    },
    "tags": {
      "region": "us-west"
    }
  }'
```

创建端点：
```bash
curl -X POST http://localhost:8080/api/v1/endpoints \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-endpoint",
    "port": 8081,
    "platform_id": "my-platform",
    "enabled": true
  }'
```

### 服务管理

#### 服务控制

```bash
# 启动服务
sudo systemctl start prism

# 停止服务
sudo systemctl stop prism

# 重启服务
sudo systemctl restart prism

# 查看状态
sudo systemctl status prism

# 查看日志
sudo journalctl -u prism -f
```

#### 备份与恢复

创建备份：
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -c
```

从备份恢复：
```bash
./scripts/prism-backup.sh restore -d ./state -f ./backups/backup-20260912.tar.gz
```

### API 文档

完整的 API 文档在 Prism 运行时可通过 `/ui/docs` 访问。

主要端点：
- `GET /api/v1/platforms` - 列出所有平台
- `POST /api/v1/nodes` - 添加节点到节点池
- `GET /api/v1/endpoints` - 列出端点
- `GET /api/v1/metrics/snapshots/summary` - 获取指标摘要
- `POST /api/v1/quality/probe` - 探测节点质量

### 开发

#### 项目结构

```
Prism/
├── cmd/prism/          # 主应用程序入口
├── internal/           # 内部包
│   ├── api/           # REST API 处理器
│   ├── config/        # 配置管理
│   ├── node/          # 节点池和管理
│   ├── outbound/      # 代理协议实现
│   ├── platform/      # 平台逻辑
│   ├── quality/       # 质量监控系统
│   ├── rotation/      # 定时轮换
│   └── service/       # 控制平面服务
├── web/               # Web UI（嵌入式）
├── scripts/           # 部署和工具脚本
└── docs/              # 文档
```

#### 从源码构建

```bash
# 构建
go build -buildvcs=false ./cmd/prism

# 运行测试（需要测试文件）
go test ./...

# 使用优化选项构建
go build -ldflags="-s -w" -buildvcs=false ./cmd/prism
```

### 安全性

- 所有 API 端点都需要 token 认证
- Token 安全生成并以受限权限存储
- 状态数据库包含敏感配置 - 请保护访问权限
- 生产环境建议使用 TLS 反向代理
- 建议定期进行安全更新

### 性能

Prism 专为高性能设计：
- 每个端点可处理 10,000+ 并发连接
- 亚毫秒级路由决策
- 最小内存占用（基础约 50MB）
- 高效的连接池和复用

### 许可证

MIT License - 详见 LICENSE 文件

### 贡献

欢迎贡献！请：
1. Fork 本仓库
2. 创建功能分支
3. 进行修改
4. 提交 Pull Request

### 支持

- GitHub Issues: https://github.com/ermitcc/Prism/issues
- 文档: https://github.com/ermitcc/Prism/tree/main/docs

