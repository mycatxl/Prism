# Scheduled Rotation

## Overview

Scheduled rotation automatically rotates sticky session leases at configured intervals to provide better IP rotation for platforms that require it. This feature is useful for scenarios where you want to enforce regular IP changes regardless of client activity.

## How It Works

The `ScheduledRotator` runs as a background task that periodically scans all platforms with scheduled rotation enabled. For each platform:

1. It identifies leases that have exceeded the configured rotation interval
2. It deletes those leases, forcing clients to get a new IP on their next request
3. Processing happens in parallel across platforms using a worker pool

## Configuration

Scheduled rotation is configured per-platform via the `/api/v1/platforms/{id}` PATCH endpoint (management calls need the admin bearer token):

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
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/my-platform-id \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "scheduled_rotation_enabled": true,
    "scheduled_rotation_interval": "1h"
  }'
```

### Disable rotation

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/my-platform-id \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN" \
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

- `internal/routing/scheduled_rotator.go` - Main implementation.
- `internal/platform/platform.go` - Configuration fields.
- `internal/model/models.go` - Persisted platform fields (`scheduled_rotation_enabled`, `scheduled_rotation_interval`).
- `cmd/prism/app_runtime.go` - Construction (`routing.NewScheduledRotator`, line 269) and start/stop with the router (line 448).

### Initialization

The rotator is created with the router and runs for the lifetime of the service:

```go
topoRuntime.rotator = routing.NewScheduledRotator(topoRuntime.router, topoRuntime.pool)
...
topoRuntime.rotator.Start()   // stopped again on shutdown
```

## Testing

There is no automated test for the rotator in this repository yet: the
`internal/routing` package has no `_test.go` file. Deriving the sweep behaviour
from the implementation (`internal/routing/scheduled_rotator.go`):

- a platform with `scheduled_rotation_enabled=false` is a no-op;
- only leases whose `created_at` is older than `scheduled_rotation_interval` are
  deleted;
- platforms are swept in parallel, with jitter around the 7 second period;
- `Stop()` terminates the sweep goroutine.

End-to-end behaviour (a lease being replaced after the interval) is currently
only covered manually through the console/log output of a running instance.
