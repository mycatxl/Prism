# Prism compatibility patch

Base: `github.com/sagernet/wireguard-go v0.0.1-beta.7`, commit
`0a7ffb1ab95e8305cddb1e43ff454b7e06c57c66`.

Prism keeps this source in-tree because sing-box v1.12.21 requires the legacy
`conn.Listener` and `Bind.Send(bufs, endpoint)` interfaces. All available newer
WireGuard releases that fix the shutdown lock inversion use the incompatible
`Bind.Send(bufs, endpoint, offset)` interface.

The only source change is in `device/device.go`, `Device.Close`: acquire
`device.state` before `device.ipcMutex`, matching `changeState` / `upLocked`.
The original opposite order deadlocks when Close races with a TUN-up event.
This ordering is also present in upstream v0.0.2-beta.1 through v0.0.5.

Validation commands from the Prism root:

```sh
node scripts/verify.mjs wireguard
node scripts/verify.mjs race
```

Results are recorded under `.local/verification/`. No transport interfaces or
protocol behavior are changed by this backport. The original license and
copyright notices are preserved in this directory.

Remove this replacement when upgrading sing-box to a version compatible with
the newer WireGuard transport API, after repeating the protocol/lifecycle tests.
