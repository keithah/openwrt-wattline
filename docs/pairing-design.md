# GUI pairing flow — design

Status: implementing (2026-07-16). Companion to the LTK root-cause investigation
in `continue.md`; the API/UI here is required regardless of that bug's cause.

## Problem

`wattlined` registers a BlueZ pairing agent but never initiates pairing
(`org.bluez.Device1.Pair`). Pairing today requires SSH + `bluetoothctl`. The
Link-Power's protected GATT ops need an authenticated bond, so first-run setup
must be: scan → select device → pair with PIN → trust → connect, driven from
the router GUI (GL panel and LuCI).

## Daemon design

### New pieces

- `internal/ble/pairing.go` — `Pairing` manager: a small async state machine
  (`idle → scanning → pairing → paired | error`) exposed to the API. It holds
  the latest scan results and one in-flight operation; concurrent starts are
  rejected with `ErrBusy`.
- `PairOps` interface abstracts the BlueZ primitives so the manager is unit
  testable off-Linux:
  - `Scan(dur) ([]Found, error)` — discovery filtered to `Link-Power*` /
    `PeakDo-OTA*` advertisements (fresh adv name, per API.md §12).
  - `Pair(mac string) error` — remove stale bond first, then `Device1.Pair`.
    Retries once after `CancelPairing` (observed BlueZ stale-request pattern:
    first pair can hang; cancel + immediate retry succeeds).
  - `Trust(mac string) error` — `Device1.Trusted = true`.
  - `Unpair(mac string) error` — `Adapter1.RemoveDevice`.
- `internal/ble/bluez.go` (linux) — `PairOps` implemented over godbus, same
  bus/style as `agent.go`.
- Agent PIN becomes settable (`SetAgentPIN`) so a pair request may carry a
  PIN different from the configured one; on success the pair flow persists
  `device_mac` and `pin` to UCI (`config.SavePairing`).
- `Transport.Close()` — the connector must release the radio while pairing
  (Link-Power accepts one central); the pairing manager pauses the connector
  (`Connector.Pause/Resume`), closing any live session.

### API (bearer-token, same auth as the rest)

| Endpoint | Effect |
|---|---|
| `POST /api/v1/pairing/scan` | start async scan (~12 s); 202, or 409 if busy |
| `GET  /api/v1/pairing/status` | `{stage, error?, devices: [{mac,name,rssi,paired}]}` |
| `POST /api/v1/pairing/pair` `{mac, pin?}` | start async pair+trust; 202/409 |
| `DELETE /api/v1/pairing/device/{mac}` | remove bond |

Rationale for async + polling: pairing takes 5–30 s; GL panel and LuCI both
poll comfortably, and no long-held HTTP requests hit the router's httpd.

### Success criteria (from continue.md)

`Paired`/`Bonded` alone is not success — BlueZ has been observed reporting a
transient `Paired: yes` with no stored LTK on this hardware. The manager
therefore verifies before claiming success: after Pair+Trust it resumes the
connector and blocks in `WaitConnected` (60 s) until a session is established
— which requires the protected `0x4301` INFO write in the handshake to
succeed. Only then does it persist `device_mac`/`pin` to UCI and report
`paired`; otherwise it reports `error` with a hint to unpair the device from
other hosts, and rolls back any PIN override so a typo'd GUI PIN never
outlives its attempt.

## UI

- **GL panel** (`gl-app-wattline`): add a "Device" card to the existing view —
  when disconnected, show Scan button → device list (name, MAC, RSSI) → PIN
  field (prefilled 020555) → Pair; poll status; finish on connected.
- **LuCI** (`luci-app-wattline`): same flow on the settings page, plain
  `fetch` polling like the existing views.

## Open question feeding back into this design

The LTK root-cause capture (btmon on the GL-X3000) may show the peripheral
negotiating non-bonding (e.g. single bond slot already held by another host).
If so, the pair flow gains a user-facing hint: "unpair the device from other
hosts (phone/laptop) before pairing the router."
