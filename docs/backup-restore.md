# Backup and Restore Tools

## Overview

The Prism backup and restore tools allow you to save and restore the complete state of your Prism instance, including:
- Platform configurations
- Endpoint definitions
- Node pool state
- Sticky session leases
- Routing state
- Metrics data

## Usage

### Backup Script

The backup script is located at `/scripts/prism-backup.sh`.

#### Create a Backup

Basic backup:
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups
```

Compressed backup:
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -c
```

Named backup:
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -n production-before-upgrade -c
```

#### Restore from Backup

```bash
./scripts/prism-backup.sh restore -b ./backups/backup-20260912-143022.tar.gz -d ./state
```

Force restore without confirmation:
```bash
./scripts/prism-backup.sh restore -b ./backups/backup-20260912-143022.tar.gz -d ./state -f
```

#### List Backups

```bash
./scripts/prism-backup.sh list -o ./backups
```

Example output:
```
[INFO] Backups in ./backups:

  [1] backup-20260912-143022.tar.gz
      Size: 2.4M
      Date: 2026-09-12 14:30:22
      Meta: { "backup_name": "backup-20260912-143022", ... }

  [2] production-before-upgrade.tar.gz
      Size: 2.5M
      Date: 2026-09-11 09:15:33

[INFO] Total backups: 2
```

#### Clean Old Backups

Keep only the 5 most recent backups:
```bash
./scripts/prism-backup.sh clean -o ./backups -k 5
```

Keep default 10 backups:
```bash
./scripts/prism-backup.sh clean -o ./backups
```

## Options

### Backup Options

| Option | Description | Default |
|--------|-------------|---------|
| `-d, --dir <path>` | State directory to backup | `./state` |
| `-o, --output <path>` | Output directory for backups | `./backups` |
| `-n, --name <name>` | Custom backup name | Timestamp |
| `-c, --compress` | Compress with gzip | Disabled |

### Restore Options

| Option | Description | Default |
|--------|-------------|---------|
| `-b, --backup <path>` | Backup file to restore | Required |
| `-d, --dir <path>` | State directory to restore to | `./state` |
| `-f, --force` | Skip confirmation prompt | Disabled |

### List Options

| Option | Description | Default |
|--------|-------------|---------|
| `-o, --output <path>` | Backup directory to list | `./backups` |

### Clean Options

| Option | Description | Default |
|--------|-------------|---------|
| `-o, --output <path>` | Backup directory to clean | `./backups` |
| `-k, --keep <number>` | Number of backups to keep | 10 |

## Best Practices

### Regular Backups

Create a cron job for automatic backups:
```bash
# Daily backup at 2 AM, keep 7 days
0 2 * * * /opt/Prism/scripts/prism-backup.sh backup -d /opt/Prism/state -o /opt/Prism/backups -c && \
          /opt/Prism/scripts/prism-backup.sh clean -o /opt/Prism/backups -k 7
```

### Before Major Changes

Always create a named backup before:
- Upgrading Prism
- Changing platform configurations
- Modifying the node pool
- Making infrastructure changes

Example:
```bash
./scripts/prism-backup.sh backup -d ./state -o ./backups -n before-v2-upgrade -c
```

### Storage Considerations

Backup sizes depend on your state:
- Small deployments: ~100KB - 1MB
- Medium deployments: ~1MB - 10MB
- Large deployments: ~10MB - 100MB

Compressed backups (with `-c`) typically reduce size by 60-80%.

### Testing Restores

Periodically test your backups by restoring to a test environment:
```bash
# Restore to a test directory
./scripts/prism-backup.sh restore -b ./backups/latest.tar.gz -d ./state-test -f

# Verify the restored state
./prism --state-dir ./state-test --port 9999
```

## Backup Format

Backups are standard tar archives containing:
- `/state/` - The complete state directory
  - `platforms.db` - Platform configurations
  - `endpoints.db` - Endpoint definitions
  - `pool.db` - Node pool state
  - `leases/` - Active lease data
  - `metrics/` - Metrics snapshots

Each backup includes a `.meta` file with metadata:
```json
{
  "backup_name": "backup-20260912-143022",
  "timestamp": "2026-09-12T14:30:22+00:00",
  "state_dir": "./state",
  "compressed": true,
  "size": "2.4M"
}
```

## Recovery Scenarios

### Accidental Configuration Change

```bash
# Stop Prism
systemctl stop prism

# Restore from backup
./scripts/prism-backup.sh restore -b ./backups/backup-before-change.tar.gz -d ./state -f

# Start Prism
systemctl start prism
```

### Corrupted State

```bash
# Stop Prism
systemctl stop prism

# Restore from most recent backup
./scripts/prism-backup.sh list -o ./backups
./scripts/prism-backup.sh restore -b ./backups/backup-latest.tar.gz -d ./state -f

# Start Prism
systemctl start prism
```

### Migration to New Server

On the old server:
```bash
# Create backup
./scripts/prism-backup.sh backup -d ./state -o ./backups -n migration -c

# Copy backup to new server
scp ./backups/migration.tar.gz newserver:/opt/Prism/backups/
```

On the new server:
```bash
# Restore
./scripts/prism-backup.sh restore -b ./backups/migration.tar.gz -d ./state -f

# Start Prism
./prism --state-dir ./state
```

## Troubleshooting

### "State directory does not exist"

Ensure the state directory path is correct:
```bash
ls -la ./state
```

### "Backup file does not exist"

Check the backup path:
```bash
./scripts/prism-backup.sh list -o ./backups
```

### Restore Overwrites Existing State

The script creates a safety backup automatically:
```
[INFO] Backing up existing state to: ./state.backup-20260912-143500
```

You can restore from this backup if needed:
```bash
rm -rf ./state
mv ./state.backup-20260912-143500 ./state
```

### Permission Denied

Ensure the script is executable:
```bash
chmod +x /scripts/prism-backup.sh
```

Ensure you have write access to the directories:
```bash
ls -la ./backups
ls -la ./state
```
