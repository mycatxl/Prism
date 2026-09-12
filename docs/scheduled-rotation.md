# Scheduled Rotation

## Overview

Scheduled rotation automatically rotates sticky session leases at configured intervals to provide better IP rotation for platforms that require it. This feature is useful for scenarios where you want to enforce regular IP changes regardless of client activity.

## How It Works

The `ScheduledRotator` runs as a background task that periodically scans all platforms with scheduled rotation enabled. For each platform:

1. It identifies leases that have exceeded the configured rotation interval
2. It deletes those leases, forcing clients to get a new IP on their next request
3. Processing happens in parallel across platforms using a worker pool

## Configuration

Scheduled rotation is configured per-platform via the `/v1/platforms/{id}` PATCH endpoint:

```json
{
  "scheduled_rotation_enabled": true,
  "scheduled_rotation_interval": "1h"
}
```

### Configuration Fields

- **`scheduled_rotation_enabled`** (boolean): Enable/disable scheduled rotation for this platform
- **`scheduled_rotation_interval`** (duration string): How long a lease can live before being rotated
  - Examples: `"30m"`, `"1h"`, `"2h30m"`
  - Must be non-negative

## Behavior

### Rotation Logic

A lease is rotated when:
```
current_time - lease_created_at >= rotation_interval
```

The rotation interval is compared against the lease's `created_at` timestamp, NOT the `last_accessed` timestamp. This ensures leases are rotated based on their age, regardless of how actively they're being used.

### What Happens During Rotation

1. The lease is deleted from the routing table
2. A `LeaseRemove` event is emitted
3. The next request from that account will create a new lease with a new node/IP

### Performance Characteristics

- **Sweep frequency**: Every 7 seconds (±3 seconds jitter)
- **Parallel processing**: Uses `runtime.GOMAXPROCS(0)` workers
- **Non-blocking**: Rotation happens in the background, no impact on request routing

## Example Usage

### Enable hourly rotation for a platform

```bash
curl -X PATCH http://localhost:8080/v1/platforms/my-platform-id \
  -H "Content-Type: application/json" \
  -d '{
    "scheduled_rotation_enabled": true,
    "scheduled_rotation_interval": "1h"
  }'
```

### Disable rotation

```bash
curl -X PATCH http://localhost:8080/v1/platforms/my-platform-id \
  -H "Content-Type: application/json" \
  -d '{
    "scheduled_rotation_enabled": false
  }'
```

## Integration with Other Features

### Sticky Sessions

Scheduled rotation works alongside sticky session TTL:
- **Sticky TTL** controls when an inactive lease expires naturally
- **Scheduled rotation** enforces a maximum lease lifetime regardless of activity

If both are configured, the shorter duration takes effect.

### Lease Cleaner

The `LeaseCleaner` removes expired leases based on their expiry timestamp. Scheduled rotation is independent and removes leases based on age. Both can run simultaneously.

## Monitoring

### Lease Events

Each rotation emits a `LeaseRemove` event with:
- `platform_id`: The platform ID
- `account`: The account identifier
- `node_hash`: The rotated node hash
- `egress_ip`: The rotated egress IP
- `created_at_ns`: Original lease creation timestamp

These events can be observed via the lease event callback configured in the router.

## Implementation Details

### Architecture

```
┌─────────────────────┐
│ ScheduledRotator    │
│  - sweep() every 7s │
└──────────┬──────────┘
           │
           ├─→ Platform 1 (worker)
           ├─→ Platform 2 (worker)
           └─→ Platform N (worker)
                    │
                    ├─→ Collect old leases
                    └─→ Delete via Router.DeleteLease()
```

### Files

- `/internal/routing/scheduled_rotator.go` - Main implementation
- `/internal/routing/scheduled_rotator_test.go` - Comprehensive tests
- `/internal/platform/platform.go` - Configuration fields
- `/cmd/prism/main.go` - Service initialization

### Initialization

The rotator is automatically started during service initialization:

```go
rotator := routing.NewScheduledRotator(router, pool)
rotator.Start()
defer rotator.Stop()
```

## Testing

Run the test suite:

```bash
go test ./internal/routing -run TestScheduledRotator -v
```

Tests cover:
- Basic rotation functionality
- Disabled rotation (no-op)
- Age-based filtering (only old leases rotated)
- Graceful shutdown
- Parallel platform processing
