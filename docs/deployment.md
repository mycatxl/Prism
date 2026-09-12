# Prism Deployment Guide

## Quick Start

Deploy Prism with management interface on port 1262:

```bash
./scripts/deploy.sh --port 1262
```

This will:
1. Check for the Prism binary
2. Verify port availability
3. Create state directory
4. Generate admin token
5. Start the service
6. Display access information

## Deployment Options

### Basic Deployment

```bash
./scripts/deploy.sh
```

Uses default configuration:
- Port: 1262
- State: `./state`
- Log level: info
- Auto-generated admin token

### Custom Port

```bash
./scripts/deploy.sh --port 8080
```

### Custom State Directory

```bash
./scripts/deploy.sh --state-dir /opt/prism/state
```

### Custom Admin Token

```bash
./scripts/deploy.sh --admin-token "your-secure-token-here"
```

### Complete Custom Configuration

```bash
./scripts/deploy.sh \
  --port 1262 \
  --state-dir /opt/prism/state \
  --admin-token "my-secure-token" \
  --log-level debug \
  --dns-upstreams "https://1.1.1.1/dns-query,https://8.8.8.8/dns-query"
```

## Environment Variables

You can also configure via environment variables:

```bash
PORT=1262 \
STATE_DIR=/opt/prism/state \
ADMIN_TOKEN=my-token \
LOG_LEVEL=info \
./scripts/deploy.sh
```

## Systemd Service

The deployment script can install Prism as a systemd service for automatic startup and management.

### Service Management

Start service:
```bash
sudo systemctl start prism
```

Stop service:
```bash
sudo systemctl stop prism
```

Restart service:
```bash
sudo systemctl restart prism
```

Check status:
```bash
sudo systemctl status prism
```

Enable autostart:
```bash
sudo systemctl enable prism
```

Disable autostart:
```bash
sudo systemctl disable prism
```

### View Logs

Real-time logs:
```bash
sudo journalctl -u prism -f
```

Recent logs:
```bash
sudo journalctl -u prism -n 100
```

Logs since boot:
```bash
sudo journalctl -u prism -b
```

## Accessing the Management Interface

After deployment, access the web interface:

```
http://localhost:1262/ui/
```

You'll need the admin token displayed after deployment. The token is also saved in:
```
<state-dir>/.admin_token
```

## API Access

The REST API is available at:

```
http://localhost:1262/api/v1/
```

Use the admin token in the Authorization header:
```bash
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:1262/api/v1/platforms
```

## Post-Deployment Steps

### 1. Create Your First Platform

Via Web UI:
1. Navigate to http://localhost:1262/ui/
2. Click "Platforms" → "Create Platform"
3. Fill in platform details
4. Enable features (sticky sessions, scheduled rotation)

Via API:
```bash
curl -X POST http://localhost:1262/api/v1/platforms \
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

### 2. Add Endpoints

```bash
curl -X POST http://localhost:1262/api/v1/endpoints \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-endpoint",
    "port": 10808,
    "platform_id": "my-platform",
    "enabled": true
  }'
```

### 3. Add Nodes to the Pool

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
      "password": "your-password"
    },
    "tags": {
      "region": "us-west",
      "provider": "aws"
    }
  }'
```

### 4. Set Up Backups

Create a backup schedule:
```bash
# Add to crontab
crontab -e

# Daily backup at 2 AM
0 2 * * * /opt/Prism/scripts/prism-backup.sh backup -d /opt/prism/state -o /opt/prism/backups -c
```

## Security Considerations

### Admin Token

The admin token grants full access to Prism. Protect it:

1. **Never commit to version control**
2. **Use strong random tokens** (64+ characters)
3. **Rotate regularly**
4. **Store securely** (e.g., environment variables, secrets manager)

### Firewall

If exposing Prism externally, configure firewall rules:

```bash
# Allow only specific IPs
sudo ufw allow from 10.0.0.0/8 to any port 1262

# Or use a reverse proxy
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
```

### Reverse Proxy

For production, use a reverse proxy with TLS:

#### Nginx Example

```nginx
server {
    listen 443 ssl http2;
    server_name prism.example.com;

    ssl_certificate /etc/ssl/certs/prism.crt;
    ssl_certificate_key /etc/ssl/private/prism.key;

    location / {
        proxy_pass http://127.0.0.1:1262;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

#### Caddy Example

```caddyfile
prism.example.com {
    reverse_proxy localhost:1262
}
```

## Updating Prism

1. Stop the service:
```bash
sudo systemctl stop prism
```

2. Create a backup:
```bash
./scripts/prism-backup.sh backup -d /opt/prism/state -o /opt/prism/backups -n before-update -c
```

3. Build new version:
```bash
git pull
go build -buildvcs=false ./cmd/prism
```

4. Start the service:
```bash
sudo systemctl start prism
```

5. Verify:
```bash
sudo systemctl status prism
curl -H "Authorization: Bearer YOUR_TOKEN" http://localhost:1262/api/v1/system/info
```

## Monitoring

### Health Check

```bash
curl http://localhost:1262/health
```

### Metrics

Access metrics via the Web UI or API:

```bash
curl -H "Authorization: Bearer YOUR_TOKEN" \
  http://localhost:1262/api/v1/metrics/snapshots/summary
```

### Prometheus Integration

Prism exposes metrics compatible with Prometheus. Add to `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'prism'
    static_configs:
      - targets: ['localhost:1262']
    metrics_path: '/api/v1/metrics/prometheus'
    authorization:
      credentials: 'YOUR_TOKEN'
```

## Troubleshooting

### Port Already in Use

```bash
# Find process using port
lsof -i :1262

# Kill process
kill $(lsof -t -i:1262)

# Or use a different port
./scripts/deploy.sh --port 8080
```

### Permission Denied

```bash
# Ensure script is executable
chmod +x ./scripts/deploy.sh

# Check state directory permissions
ls -la ./state
chmod 755 ./state
```

### Service Won't Start

Check logs:
```bash
sudo journalctl -u prism -n 50
```

Check configuration:
```bash
# View service file
cat /etc/systemd/system/prism.service

# Validate
sudo systemctl daemon-reload
sudo systemctl status prism
```

### Can't Access Web UI

1. Verify service is running:
```bash
sudo systemctl status prism
```

2. Check if port is listening:
```bash
netstat -tlnp | grep 1262
```

3. Test API directly:
```bash
curl http://localhost:1262/health
```

4. Check firewall:
```bash
sudo ufw status
```

### Authentication Failed

1. Verify token:
```bash
cat /opt/prism/state/.admin_token
```

2. Test with curl:
```bash
TOKEN=$(cat /opt/prism/state/.admin_token)
curl -H "Authorization: Bearer $TOKEN" http://localhost:1262/api/v1/platforms
```

3. Clear browser storage and re-enter token

## Uninstalling

Stop and disable service:
```bash
sudo systemctl stop prism
sudo systemctl disable prism
sudo rm /etc/systemd/system/prism.service
sudo systemctl daemon-reload
```

Remove files:
```bash
rm -rf /opt/Prism
```

Or keep backups:
```bash
# Keep backups and state
mv /opt/Prism/backups ~/prism-backups
mv /opt/Prism/state ~/prism-state
rm -rf /opt/Prism
```
