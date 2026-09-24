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
  "scheduled_rotation_interval": "1h",
  "rotation_avoid_previous_ip": true
}
```

### Configuration Fields

- **`scheduled_rotation_enabled`** (boolean): Enable/disable scheduled rotation for this platform
- **`scheduled_rotation_interval`** (duration string): How long a lease can live before being rotated
  - Examples: `"30m"`, `"1h"`, `"2h30m"`
  - Must be non-negative
- **`rotation_avoid_previous_ip`** (boolean, default `true`): Exclude the account's previous egress IP from the next lease

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

### Rotation Tombstones (Avoid the Previous Egress IP)

Every rotation remembers the egress IP the account was using, so the next lease can avoid handing back the same IP:

1. The rotator (or the manual rotate endpoint) deletes the lease and writes an in-memory *rotation tombstone* keyed by `platformID + "\x00" + account`, holding the previous egress IP and an expiry of `max(scheduled_rotation_interval, sticky_ttl)`.
2. When the account's next lease is created and the platform has `rotation_avoid_previous_ip` enabled, nodes whose egress IP equals the tombstone IP are excluded from the P2C candidate set.
3. If **every** remaining candidate shares that IP, the exclusion is dropped (the request is still served) and the request log records the event `rotation_fallback_same_ip`.

Operational notes:

- Tombstones are **not persisted**. They live in a bounded LRU (100,000 entries) inside `Router`; the least recently used entry is evicted when the bound is reached. Losing tombstones on restart only means an account may draw the same egress IP once more.
- Rotation avoidance needs at least two distinct egress IPs in the platform's routable view, otherwise every rotation reports `rotation_fallback_same_ip`.

### Manual Rotation

`POST /api/v1/platforms/{id}/leases/{account}/actions/rotate` performs the same "delete lease + write tombstone" step on demand and returns `204 No Content` (it returns `404 NOT_FOUND` when the account holds no lease).

```bash
curl -X POST http://127.0.0.1:2260/api/v1/platforms/my-platform-id/leases/alice/actions/rotate \
  -H "Authorization: Bearer $PRISM_ADMIN_TOKEN"
```

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
                    ├─→ Delete via Router.DeleteLeaseIfOlderThan()
                    └─→ Record rotation tombstone (Router.recordRotationTombstone)
```

### Files

- `internal/routing/scheduled_rotator.go` - Main implementation.
- `internal/routing/rotation_tombstone.go` - Tombstone LRU + rotation-avoidance lookups.
- `internal/routing/random.go` - P2C candidate exclusion (`randomRouteAvoiding`).
- `internal/platform/model_codec.go` - Carries the rotation fields onto runtime platforms.
- `internal/platform/platform.go` - Configuration fields.
- `internal/model/models.go` - Persisted platform fields (`scheduled_rotation_enabled`, `scheduled_rotation_interval`, `rotation_avoid_previous_ip`).
- `cmd/prism/app_runtime.go` - Construction (`routing.NewScheduledRotator`) and start/stop with the router.

### Initialization

The rotator is created with the router and runs for the lifetime of the service:

```go
topoRuntime.rotator = routing.NewScheduledRotator(topoRuntime.router, topoRuntime.pool)
...
topoRuntime.rotator.Start()   // stopped again on shutdown
```

## Testing

Automated coverage lives next to the implementation:

- `internal/routing/scheduled_rotator_test.go` - sweep/stop behaviour, disabled platforms, old-vs-new leases.
- `internal/routing/rotation_tombstone_test.go` - tombstone expiry and LRU eviction, ten consecutive
  rotations never repeating an egress IP, single-IP fallback recording `rotation_fallback_same_ip`,
  manual rotation via `Router.RotateLease`.
- `internal/service/control_plane_platform_rotation_test.go` - the rotation fields survive
  `platformConfig.toRuntime` and platform creation publishes them to the pool.
- `internal/platform/model_codec_test.go` - `BuildFromModel` (startup path) keeps the rotation fields.
- `internal/api/lease_rotate_contract_test.go` - manual rotate endpoint contract (204/404/400/401/405).
- `internal/requestlog/events_test.go` - routing events round-trip through the request log.
