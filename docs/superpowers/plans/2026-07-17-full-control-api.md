# Wattlined Full Control API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the complete, versioned Link-Power router HTTP API, secure client enrollment and token management, HTTP/HTTPS and VPN reachability, LAN mDNS discovery, OpenWrt packaging, and matching LuCI/GL administration surfaces.

**Architecture:** Preserve the existing protocol, BLE session, state store, rules, actions, and SSE path. Add exact protocol codecs at the bottom, a testable control/reconciliation service above the BLE session, resource-oriented HTTP handlers above that, and focused auth/server/discovery packages beside the existing daemon. Keep legacy routes as adapters over canonical behavior.

**Tech Stack:** Go 1.22, `net/http`, BlueZ through `tinygo.org/x/bluetooth`, UCI text configuration, ECDSA P-256/X.509, JSON persistence, `github.com/grandcat/zeroconf@v1.0.0`, `github.com/skip2/go-qrcode@v0.0.0-20200617195104-da1b6568686e`, OpenWrt procd/firewall UCI, LuCI JavaScript, GL oui Vue 2, gzip/ustar IPKs for `aarch64_cortex-a53`.

## Global Constraints

- Treat uppercase `docs/API.md` as read-only BLE protocol truth; lowercase `docs/api.md` is the daemon HTTP contract.
- Do not touch Swift code, add cloud behavior, transmit telemetry off-router, flash OTA firmware, or implement opcodes `0x05`, `0x12`, or `0xF0`.
- Reuse and extend `internal/proto`, `internal/ble`, `internal/state`, `internal/rules`, and `internal/actions`; do not fork their logic.
- Preserve current UCI keys, legacy endpoint success shapes, rules, webhooks, history, and unnamed SSE `data:` frames.
- Write a failing unit/table test and observe the intended failure before every production behavior change.
- Keep every command-channel operation serialized; hold serialization across SET/DELETE plus authoritative re-read sequences.
- Telemetry is truth for DC output, USB-C output, and DC bypass.
- Confirm DC output only from `DcPortStatus.enabled`; confirm USB-C output only from `TypeCPortStatus.mode` transitions `3 -> 1` and `1 -> 3`; never use Type-C `enabled` for output confirmation.
- Clear USB-C limits only with `[0x02, 0x02, type]`, never opcode `0x06`; treat result `0xFF` as unset only for the read-only runtime limit.
- HTTP defaults to `8377`, HTTPS to `8378`, both on IPv4 and IPv6; WAN access defaults off.
- Keep IPKs gzip-tar, inner/outer ustar, normalized root ownership/modes, force-reinstall compatible, and `aarch64_cortex-a53`.
- The completed branch must pass uncached `go test ./...`, `make -C package all`, IPK metadata checks, and `git diff --check`.

## File Structure

New focused files:

- `docs/api.md`: authoritative REST/SSE/discovery/pairing/TLS contract.
- `internal/proto/advanced.go`: barrier-free, running mode, USB firmware, BLE PIN, and feature decoding.
- `internal/proto/lifecycle.go`: OTA INFO, OTA enter/exit, restart/shutdown, and Current Time codecs.
- `internal/state/device.go`: identity, capabilities, connection state, and bounded command records.
- `internal/control/service.go`: session resolution, capability/policy checks, and telemetry reconciliation.
- `internal/control/timers.go`: timer validation and authoritative mutation/re-list behavior.
- `internal/auth/store.go`: hashed managed-token persistence and last-seen coalescing.
- `internal/auth/pairing.go`: enrollment mode, rotating PIN, TTL, and rate limits.
- `internal/api/errors.go`: canonical JSON errors.
- `internal/api/device.go`, `timers.go`, `advanced.go`, `admin.go`: canonical resource handlers.
- `internal/server/cert.go`, `listeners.go`: initialization, certificate fingerprinting, and dual-stack HTTP/HTTPS lifecycle.
- `internal/discovery/txt.go`, `service.go`, `tailscale.go`: TXT generation, LAN-only zeroconf, and optional MagicDNS detection.
- `package/wattlined/usr/lib/wattline/firewall-sync`: Wattline-owned WAN rule reconciliation.
- `package/wattlined/etc/hotplug.d/iface/95-wattline`: listener/firewall refresh when relevant interfaces change.

Existing files modified in place:

- `internal/proto/{frames.go,controls.go,schedule.go}` and tests for shared constants and validation.
- `internal/ble/{transport.go,tinygo.go,session.go,controls.go,connector.go}` and tests for discovered characteristics, bootloader sessions, serialized sequences, and reconnect policy.
- `internal/state/store.go` and tests to publish enriched snapshots without breaking old fields.
- `internal/api/server.go` and existing tests to register canonical routes and role-aware middleware while retaining aliases.
- `internal/config/config.go`, UCI defaults, procd, package controls, Makefile, LuCI, GL panel, and README.

---

## Milestone 1 — Protocol, BLE, state, and canonical device API

### Task 1: Write the HTTP contract before handlers

**Files:**
- Create: `docs/api.md`
- Modify: `docs/superpowers/specs/2026-07-17-full-control-api-design.md` only if an unavoidable contradiction is found

**Interfaces:**
- Consumes: approved design and existing route inventory from `internal/api/server.go`.
- Produces: exact request/reply/auth/error/SSE/mDNS/pairing/TLS contract used by all later handler tests.

- [ ] **Step 1: Create the route and policy inventory**

Write `docs/api.md` with these top-level sections in this order: versioning/base URLs; authentication roles; error envelope; device identity; telemetry/history/SSE; granular controls; timers; OTA/clock/advanced; rules and legacy actions; BLE-device pairing; API-client pairing; tokens; settings/TLS; mDNS; QR payload; compatibility routes; on-target caveats. Include every route from `internal/api/server.go` and every canonical route from the design specification.

- [ ] **Step 2: Lock canonical examples**

For each endpoint, include method/path, required role, request JSON, success status and reply JSON, all endpoint-specific errors, and whether BLE I/O occurs. Specify SSE exactly as:

```text
Content-Type: text/event-stream

data: {complete snapshot JSON}\n
\n
```

Specify mDNS TXT keys exactly as `ver`, `api`, `id`, `model`, `cid`, `features`, `tls`, and `auth` and specify the `wattline://pair?v=1...` escaping and omission rules.

- [ ] **Step 3: Review the contract against both sources of truth**

Run:

```bash
rg -n '^##|^###|/api/v1/|_wattline|wattline://|data:' docs/api.md
rg -o '"[A-Z]+ /api/v1/[^" ]+' internal/api/*.go | sort -u
```

Expected: every existing route is represented, canonical routes have exact JSON, and no OTA flashing route exists.

- [ ] **Step 4: Commit**

```bash
git add docs/api.md
git commit -m "Document wattlined HTTP API contract"
```

### Task 2: Complete exact protocol codecs

**Files:**
- Create: `internal/proto/advanced.go`
- Create: `internal/proto/advanced_test.go`
- Create: `internal/proto/lifecycle.go`
- Create: `internal/proto/lifecycle_test.go`
- Modify: `internal/proto/controls.go`
- Modify: `internal/proto/controls_test.go`
- Modify: `internal/proto/frames.go`
- Modify: `internal/proto/frames_test.go`
- Modify: `internal/proto/schedule.go`
- Modify: `internal/proto/schedule_test.go`

**Interfaces:**
- Consumes: exact bytes in `docs/API.md` sections 3–5 and 8–9.
- Produces: pure functions `BarrierFreeGet`, `BarrierFreeSet`, `RunningModeSet`, `USBFirmwareGet`, `BLEPINSet`, `ParseUSBFirmware`, `DecodeFeatures`, `OTAEnter`, `OTAExit`, `ParseOTAInfo`, `CurrentTimeAt`, and validated timer/limit helpers.

- [ ] **Step 1: Write failing frame tables**

Add table tests containing at least these fixtures:

```go
tests := []struct{name string; got, want []byte}{
    {"barrier get", BarrierFreeGet(), []byte{0x03, 0x00}},
    {"barrier on", BarrierFreeSet(true), []byte{0x03, 0x01, 0x01}},
    {"pin 020555", BLEPINSet(20555), []byte{0x04, 0x01, 0x4b, 0x50, 0x00, 0x00}},
    {"running factory", RunningModeSet(1), []byte{0xe0, 0x01, 0x01}},
    {"usb firmware", USBFirmwareGet(), []byte{0x17, 0x00}},
    {"ota enter", OTAEnter(), []byte{'P', 'K'}},
    {"ota exit", OTAExit(), []byte{0x83}},
}
```

Add feature-table assertions for all bits 0–14, OTA app/bootloader INFO fixtures from `docs/API.md`, Current Time reason `0` and `1`, limit validation, and all timer recurrence/status rules.
Also assert `ValidateReply(RunningModeSet(1), []byte{0xe0, 0x81, 0x00})` succeeds; `e0 81 00` is a SET reply, not evidence of a readable running-mode state.

- [ ] **Step 2: Verify red**

Run:

```bash
go test ./internal/proto/ -run 'Test(Advanced|Lifecycle|Feature|Timer|Limit)' -count=1
```

Expected: compile failures for the new functions and validation types.

- [ ] **Step 3: Implement the pure codecs**

Use these signatures:

```go
type FeatureSet struct {
    Display, FactoryMode, Sleep, Shutdown, BatteryCapacity bool
    DCOutPort, DCOutControl, DCOutScheduler bool
    USBPort, USBPowerLimit, USBOutputControl bool
    DCBypass, DCBypassControl, USBDCInput, USBDCInputPower bool
}
func DecodeFeatures(raw uint32) FeatureSet
func BarrierFreeGet() []byte
func BarrierFreeSet(on bool) []byte
func RunningModeSet(mode byte) []byte
func USBFirmwareGet() []byte
func ParseUSBFirmware(payload []byte) ([]byte, error)
func BLEPINSet(pin uint32) []byte
func OTAEnter() []byte
func OTAExit() []byte
type OTAInfo struct { Mode byte; OTAStart uint32; BlockSize, ChipType, CID uint16; AppStart uint32; Revision byte }
func ParseOTAInfo([]byte) (OTAInfo, error)
func CurrentTimeAt(time.Time, byte) []byte
```

Make `CurrentTime(t)` call `CurrentTimeAt(t, 0)` only for compatibility; handshake code will explicitly request reason `1`. Reject BLE PIN values above `999999`, timer status outside `1/-1` for writes, invalid recurrence masks/dates, limit levels outside `0..5`, and writes to runtime limits.

- [ ] **Step 4: Verify green**

```bash
go test ./internal/proto/ -count=1
```

Expected: all protocol tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/proto
git commit -m "Complete Link-Power protocol codecs"
```

### Task 3: Extend state with identity and command transitions

**Files:**
- Create: `internal/state/device.go`
- Create: `internal/state/device_test.go`
- Modify: `internal/state/store.go`
- Modify: `internal/state/store_test.go`

**Interfaces:**
- Consumes: `proto.FeatureSet` and existing telemetry structs.
- Produces: `state.Identity`, `state.Connection`, `state.Command`, and waitable snapshot mutation methods used by BLE, control, API, discovery, and SSE.

- [ ] **Step 1: Write failing state tests**

Cover old snapshot JSON compatibility, identity mutation, connection phases, a command `pending -> confirmed`, timeout/failure, subscriber notification on every transition, waiting on a predicate with context cancellation, and eviction after 32 terminal commands.

Use this command fixture:

```go
cmd := Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending,
    Requested: map[string]any{"on": true}, StartedAt: now, UpdatedAt: now}
```

- [ ] **Step 2: Verify red**

```bash
go test ./internal/state/ -run 'Test(Identity|Connection|Command|Wait)' -count=1
```

Expected: compile failures for new state types/methods.

- [ ] **Step 3: Implement state types and methods**

Provide:

```go
type Identity struct { Model, HWRev, AppFirmware, BootloaderFirmware, MAC string; CID uint16; Features uint32; FeatureSet proto.FeatureSet; Mode string; Characteristics map[string]bool }
type Connection struct { Phase string; ReconnectArmed bool; Since time.Time; Error string }
type Command struct { ID, Operation, Phase string; Requested any; Observed any; StartedAt, UpdatedAt time.Time; Error *CommandError }
func (s *Store) SetIdentity(Identity)
func (s *Store) SetConnection(Connection)
func (s *Store) BeginCommand(Command)
func (s *Store) FinishCommand(id, phase string, observed any, err *CommandError)
func (s *Store) Wait(ctx context.Context, predicate func(Snapshot) bool) (Snapshot, error)
```

Keep existing top-level `battery`, `dc`, `typec`, `connected`, and `updated_at` JSON fields unchanged. Add `device`, `connection`, `pending_commands`, and `recent_commands` fields.

- [ ] **Step 4: Verify green and race safety**

```bash
go test -race ./internal/state/ -count=1
```

Expected: all state tests pass without races.

- [ ] **Step 5: Commit**

```bash
git add internal/state
git commit -m "Track device identity and command state"
```

### Task 4: Make BLE handshake mode-aware and capability-aware

**Files:**
- Modify: `internal/ble/transport.go`
- Modify: `internal/ble/tinygo.go`
- Modify: `internal/ble/session.go`
- Modify: `internal/ble/session_test.go`
- Modify: `internal/ble/connector.go`
- Modify: `internal/ble/connector_test.go`

**Interfaces:**
- Consumes: protocol identity parsers and state identity/connection types.
- Produces: a live app or bootloader `Session`, discovered-characteristic lookup, bootloader firmware identity, and reconnect arm/disarm controls.

- [ ] **Step 1: Write failing transport/handshake tests**

Extend the fake transport with `HasChar(uuid string) bool` and per-call counters. Test app-mode identity including `0x2A26`, handshake Current Time reason `1`, conditional telemetry subscriptions, bootloader handshake success without `ErrBootloader`, missing Current Time, fresh-advertisement matching for both `Link-Power` and `PeakDo-OTA`, and connector behavior for app/OTA/restart/shutdown reconnect policies.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/ble/ -run 'Test(Handshake|Bootloader|Connector|Characteristic)' -count=1
```

Expected: interface and expectation failures.

- [ ] **Step 3: Implement characteristic and mode support**

Add:

```go
const CharFWRev = "00002a26-0000-1000-8000-00805f9b34fb"
type Transport interface {
    WriteChar(string, []byte) error
    ReadChar(string) ([]byte, error)
    Subscribe(string, func([]byte)) error
    HasChar(string) bool
    Disconnected() <-chan struct{}
    Close() error
}
func (s *Session) HasChar(uuid string) bool
func (s *Session) Mode() string
func ScanAndConnectPrefixes(prefixes []string) (Transport, error)
```

`tinygoTransport.HasChar` must consult the already discovered map and perform no GATT operation. `ScanAndConnectPrefixes` must match the fresh advertisement local name, never a cached name, and accept app or bootloader prefixes. App handshake reads identity/features and only subscribes to present telemetry. Bootloader handshake reads OTA INFO and Device Information but does not touch command/telemetry/time characteristics.

Replace the connector's fatal bootloader branch with a normal session callback. Add explicit `ArmReconnect(delay)`, `DisarmReconnect()`, and `ResumeReconnect()` behavior rather than inferring lifecycle intent from any disconnect.

- [ ] **Step 4: Verify green**

```bash
go test -race ./internal/ble/ -count=1
```

Expected: all BLE tests pass without races.

- [ ] **Step 5: Commit**

```bash
git add internal/ble
git commit -m "Support app and OTA BLE sessions"
```

### Task 5: Add atomic BLE operations and lifecycle controls

**Files:**
- Modify: `internal/ble/session.go`
- Modify: `internal/ble/controls.go`
- Create: `internal/ble/advanced.go`
- Create: `internal/ble/lifecycle.go`
- Modify: `internal/ble/controls_test.go`
- Create: `internal/ble/advanced_test.go`
- Create: `internal/ble/lifecycle_test.go`

**Interfaces:**
- Consumes: protocol codecs and mode-aware transport.
- Produces: raw session operations used by `internal/control` while preserving `actions.Device` methods.

- [ ] **Step 1: Write failing operation-sequence tests**

Test exact write/read order for limit SET+GET, limit DELETE+GET, runtime unset, timer add adopting reply ID then re-listing, timer PUT/DELETE then re-listing, barrier GET/PUT, USB firmware GET, BLE PIN PUT, clock absent zero-I/O, OTA INFO/enter/exit, and disconnect-as-success only when the disconnect channel closes within a bounded grace interval.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/ble/ -run 'Test(Atomic|Advanced|Clock|OTA|Disconnect)' -count=1
```

Expected: missing methods and non-atomic current behavior failures.

- [ ] **Step 3: Implement locked sequences**

Split `command` into locked and locking forms and expose these session methods:

```go
func (s *Session) GetUSBCLimit(int) (int, error)
func (s *Session) PutUSBCLimit(int, int) (int, error)
func (s *Session) DeleteUSBCLimit(int) (int, error)
func (s *Session) ListTimers() ([]proto.Timer, error)
func (s *Session) AddTimer(proto.Timer) ([]proto.Timer, byte, error)
func (s *Session) PutTimer(byte, proto.Timer) ([]proto.Timer, error)
func (s *Session) DeleteTimer(byte) ([]proto.Timer, error)
func (s *Session) BarrierFree() (bool, error)
func (s *Session) SetBarrierFree(bool) (bool, error)
func (s *Session) SetRunningMode(byte) error
func (s *Session) USBFirmwareVersion() ([]byte, error)
func (s *Session) SetBLEPIN(uint32) error
func (s *Session) ReadClock() (time.Time, bool, error)
func (s *Session) SyncClock(time.Time, byte) error
func (s *Session) OTAInfo() (proto.OTAInfo, error)
func (s *Session) EnterOTA(context.Context) error
func (s *Session) ExitOTA(context.Context) error
```

Do not add ERASE/PROGRAM/VERIFY methods. Keep existing `DCControl`, `TypeCOutput`, `BypassControl`, `Restart`, and `Shutdown` signatures for the rules/actions engine.

- [ ] **Step 4: Verify green**

```bash
go test -race ./internal/ble/ -count=1
```

Expected: exact-frame and ordering tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/ble
git commit -m "Add atomic Link-Power BLE operations"
```

### Task 6: Build the telemetry-truth control service

**Files:**
- Create: `internal/control/service.go`
- Create: `internal/control/service_test.go`
- Create: `internal/control/timers.go`
- Create: `internal/control/timers_test.go`

**Interfaces:**
- Consumes: a resolver returning the current BLE session, state store, advanced policy, and connector lifecycle controls.
- Produces: context-aware methods used directly by canonical API handlers.

- [ ] **Step 1: Write failing reconciliation tables**

Use a fake session and real state store. Prove DC ignores unrelated/bypass telemetry and confirms from `DC.Enabled`; USB-C confirms off only at mode `1` and on only at mode `3` regardless of `Enabled`; bypass ignores BLE result and confirms from `DC.Bypass`; all three expose pending then confirmed/timeout; limits and timers return session re-read results; advanced policy/capability errors are distinct.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/control/ -count=1
```

Expected: package or symbol-not-found failure.

- [ ] **Step 3: Implement service boundaries**

Define:

```go
var ErrDisconnected, ErrUnsupported, ErrAdvancedDisabled, ErrTimeout error
type Session interface {
    DCControl(bool) error
    TypeCOutput(bool) error
    BypassControl(bool) error
    GetUSBCLimit(int) (int, error)
    PutUSBCLimit(int, int) (int, error)
    DeleteUSBCLimit(int) (int, error)
    BypassThreshold() (float64, error)
    SetBypassThreshold(float64) error
    ListTimers() ([]proto.Timer, error)
    AddTimer(proto.Timer) ([]proto.Timer, byte, error)
    PutTimer(byte, proto.Timer) ([]proto.Timer, error)
    DeleteTimer(byte) ([]proto.Timer, error)
    BarrierFree() (bool, error)
    SetBarrierFree(bool) (bool, error)
    SetRunningMode(byte) error
    USBFirmwareVersion() ([]byte, error)
    SetBLEPIN(uint32) error
    ReadClock() (time.Time, bool, error)
    SyncClock(time.Time, byte) error
    OTAInfo() (proto.OTAInfo, error)
    EnterOTA(context.Context) error
    ExitOTA(context.Context) error
    Restart() error
    Shutdown() error
}
type Connector interface { ArmReconnect(time.Duration); DisarmReconnect(); ResumeReconnect() }
type Service struct {
    resolve func() Session
    store *state.Store
    connector Connector
    advanced func() bool
    confirmTimeout, bypassTimeout time.Duration
    now func() time.Time
    newID func() string
}
func (s *Service) SetDC(context.Context, bool) (proto.DCPort, error)
func (s *Service) SetTypeCOutput(context.Context, bool) (proto.TypeCPort, error)
func (s *Service) SetBypass(context.Context, bool) (proto.DCPort, error)
```

Add pass-through/validated methods for limits, threshold, timers, clock, lifecycle, OTA, and advanced operations. Generate command IDs with `crypto/rand`; tests inject deterministic IDs and clocks. Use a ten-second bypass timeout and a configurable shorter default for DC/USB-C tests.

- [ ] **Step 4: Verify green**

```bash
go test -race ./internal/control/ -count=1
```

Expected: all reconciliation and policy tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/control
git commit -m "Reconcile controls from device telemetry"
```

### Task 7: Add canonical errors, identity, control, and SSE endpoints

**Files:**
- Create: `internal/api/errors.go`
- Create: `internal/api/device.go`
- Create: `internal/api/device_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `internal/api/control.go`
- Modify: `internal/api/control_test.go`

**Interfaces:**
- Consumes: `*control.Service`, enriched state snapshot, existing rules/actions/pairing dependencies.
- Produces: canonical device/DC/USB-C/bypass/limit/threshold routes plus compatible aliases and unchanged SSE framing.

- [ ] **Step 1: Write failing endpoint tables**

For each canonical route test method/path, bearer auth, exact body validation, exact success JSON, disconnected 503, BLE 502, timeout 504, unsupported 409, advanced-disabled 403, and compatibility alias behavior. Add an SSE test that decodes two unnamed `data:` frames and observes `pending` then `confirmed` while old telemetry fields stay top-level.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/api/ -run 'Test(Device|DC|TypeC|Bypass|Limit|SSE|CanonicalError)' -count=1
```

Expected: missing routes/types and 404 responses.

- [ ] **Step 3: Implement canonical handlers and errors**

Register exactly:

```go
mux.HandleFunc("GET /api/v1/device", s.auth(s.device))
mux.HandleFunc("POST /api/v1/device/dc", s.auth(s.setDC))
mux.HandleFunc("POST /api/v1/device/usbc/output", s.auth(s.setTypeCOutput))
mux.HandleFunc("GET /api/v1/device/usbc/limit/{type}", s.auth(s.getLimit))
mux.HandleFunc("PUT /api/v1/device/usbc/limit/{type}", s.auth(s.putLimit))
mux.HandleFunc("DELETE /api/v1/device/usbc/limit/{type}", s.auth(s.deleteLimit))
mux.HandleFunc("POST /api/v1/device/dc/bypass", s.auth(s.setBypass))
mux.HandleFunc("GET /api/v1/device/dc/bypass/threshold", s.auth(s.getThreshold))
mux.HandleFunc("PUT /api/v1/device/dc/bypass/threshold", s.auth(s.putThreshold))
```

Map sentinel errors through one `writeError` function. Decode exactly one JSON object and reject unknown fields/trailing data. Keep legacy route response bodies intact by adapting canonical results in `control.go`.

- [ ] **Step 4: Verify green**

```bash
go test ./internal/api/ -count=1
```

Expected: all API and compatibility tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "Expose canonical device control API"
```

### Task 8: Add timer, clock, lifecycle, OTA, and advanced endpoints

**Files:**
- Create: `internal/api/timers.go`
- Create: `internal/api/timers_test.go`
- Create: `internal/api/advanced.go`
- Create: `internal/api/advanced_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/control.go`
- Modify: `internal/api/control_test.go`

**Interfaces:**
- Consumes: remaining `control.Service` methods and canonical errors.
- Produces: full canonical timer/clock/restart/shutdown/OTA/advanced surface and schedule aliases.

- [ ] **Step 1: Write failing route tables**

Cover list/get/add/edit/delete timers, assigned ID adoption, authoritative post-mutation list, each recurrence shape, invalid dates/masks/status, clock absent zero-I/O JSON, manual sync reason `0`, restart/shutdown disconnect success, OTA mode restrictions, INFO only, running mode PUT-only, barrier GET/PUT, raw USB firmware bytes rendered as hex/components, six-digit BLE PIN including leading zeros, and advanced policy.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/api/ -run 'Test(Timer|Clock|Restart|Shutdown|OTA|Advanced)' -count=1
```

Expected: 404/missing-handler failures.

- [ ] **Step 3: Register and implement exact routes**

Register these exact remaining routes:

```go
mux.HandleFunc("GET /api/v1/device/timers", s.auth(s.listTimers))
mux.HandleFunc("POST /api/v1/device/timers", s.auth(s.addTimer))
mux.HandleFunc("GET /api/v1/device/timers/{id}", s.auth(s.getTimer))
mux.HandleFunc("PUT /api/v1/device/timers/{id}", s.auth(s.putTimer))
mux.HandleFunc("DELETE /api/v1/device/timers/{id}", s.auth(s.deleteTimer))
mux.HandleFunc("GET /api/v1/device/clock", s.auth(s.getClock))
mux.HandleFunc("POST /api/v1/device/clock/sync", s.auth(s.syncClock))
mux.HandleFunc("POST /api/v1/device/restart", s.auth(s.restart))
mux.HandleFunc("POST /api/v1/device/shutdown", s.auth(s.shutdown))
mux.HandleFunc("GET /api/v1/device/ota", s.auth(s.otaInfo))
mux.HandleFunc("POST /api/v1/device/ota/enter", s.auth(s.enterOTA))
mux.HandleFunc("POST /api/v1/device/ota/exit", s.auth(s.exitOTA))
mux.HandleFunc("PUT /api/v1/device/advanced/running-mode", s.auth(s.putRunningMode))
mux.HandleFunc("GET /api/v1/device/advanced/barrier-free", s.auth(s.getBarrierFree))
mux.HandleFunc("PUT /api/v1/device/advanced/barrier-free", s.auth(s.putBarrierFree))
mux.HandleFunc("GET /api/v1/device/advanced/usb-fw-version", s.auth(s.getUSBFirmware))
mux.HandleFunc("PUT /api/v1/device/advanced/ble-pin", s.auth(s.putBLEPIN))
```

Return only device-observed timer collections and control results. At this milestone the existing `auth` middleware accepts only the bootstrap token, so gated routes are effectively admin-only. Task 12 replaces their wrappers with explicit role-aware `admin` middleware, including clock and bypass-threshold routes. Do not register any firmware flashing route.

- [ ] **Step 4: Verify green and full Milestone 1**

```bash
go test -race ./internal/proto/ ./internal/state/ ./internal/ble/ ./internal/control/ ./internal/api/ -count=1
```

Expected: all Milestone 1 packages pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "Expose timers lifecycle and advanced API"
```

## Milestone 2 — Configuration, pairing, tokens, TLS, and listeners

### Task 9: Expand UCI configuration without breaking existing keys

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `package/wattlined/etc/config/wattline`

**Interfaces:**
- Consumes: existing `port`, `lan_api`, `pin`, and `token` semantics.
- Produces: typed listener/TLS/auth/discovery/firewall settings with compatibility defaults.

- [ ] **Step 1: Write failing load/save tables**

Test an old five-key config and a complete new config. Assert old `pin` populates `BLEPIN`, old `port` remains HTTP port, `lan_api=1` maps to all-interface defaults, and rule sections survive. Test invalid ports, TTL, listener combinations, and paths.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/config/ -run 'Test(ConfigCompatibility|NetworkConfig|SecurityConfig)' -count=1
```

Expected: missing fields/default failures.

- [ ] **Step 3: Implement typed configuration**

Add these typed fields while preserving `PIN`, `Port`, and `LANAPI` until all callers migrate:

```go
BLEPIN string
HTTPEnabled bool
HTTPAddr4, HTTPAddr6 string
HTTPPort int
HTTPSEnabled bool
HTTPSAddr4, HTTPSAddr6 string
HTTPSPort int
TLSCert, TLSKey, TokenStore string
PairingTTL time.Duration
PairingAlwaysOn, Advanced, MDNSEnabled, WANAccess bool
MDNSInterfaces []string
```

Map UCI keys `pin`, `http_enabled`, `http_addr4`, `http_addr6`, existing `port`, `https_enabled`, `https_addr4`, `https_addr6`, `https_port`, `tls_cert`, `tls_key`, `token_store`, `pairing_ttl`, `pairing_always_on`, `advanced`, `mdns_enabled`, list `mdns_interface`, and `wan_access`. When the new HTTP address keys are absent, derive them from existing `lan_api`. Add `SaveMain(path string) error` that updates only Wattline main-section options and preserves rule and unknown sections. Expose a safe settings view that never includes bearer secrets or private-key bytes.

- [ ] **Step 4: Verify green**

```bash
go test ./internal/config/ -count=1
```

Expected: old and new UCI fixtures pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config package/wattlined/etc/config/wattline
git commit -m "Add network and security UCI settings"
```

### Task 10: Implement hashed managed-token storage

**Files:**
- Create: `internal/auth/store.go`
- Create: `internal/auth/store_test.go`

**Interfaces:**
- Consumes: bootstrap token and configurable JSON path.
- Produces: role-bearing authentication, one-time token issuance, metadata listing, revocation, and coalesced last-seen persistence.

- [ ] **Step 1: Write failing store tests**

Cover random 32-byte tokens, SHA-256-only disk content, mode `0600`, atomic temp-file rename, bootstrap metadata/non-revocation, constant-time successful lookup path, client role, label limits, duplicate IDs, revoke-immediately behavior, corrupt-file failure, and last-seen writes no more often than once per hour.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/auth/ -run 'TestToken' -count=1
```

Expected: package missing.

- [ ] **Step 3: Implement the store**

Provide:

```go
type Role string
const (RoleAdmin Role = "admin"; RoleClient Role = "client")
type Principal struct { TokenID string; Role Role }
type TokenMeta struct { ID, Label string; CreatedAt time.Time; LastSeenAt *time.Time; Bootstrap bool }
type Option func(*Store)
func OpenStore(path, bootstrap string, opts ...Option) (*Store, error)
func (s *Store) Authenticate(secret string) (Principal, bool)
func (s *Store) Issue(label string) (secret string, meta TokenMeta, err error)
func (s *Store) List() []TokenMeta
func (s *Store) Revoke(id string) error
```

Persist lowercase hex hashes, never plaintext managed secrets.

- [ ] **Step 4: Verify green and race safety**

```bash
go test -race ./internal/auth/ -run 'TestToken' -count=1
```

Expected: all token tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/store.go internal/auth/store_test.go
git commit -m "Add managed API token store"
```

### Task 11: Implement short-lived client enrollment

**Files:**
- Create: `internal/auth/pairing.go`
- Create: `internal/auth/pairing_test.go`

**Interfaces:**
- Consumes: token store, TTL/always-on config, secure random source, clock, and requester identity.
- Produces: admin-opened pairing mode and unauthenticated PIN exchange.

- [ ] **Step 1: Write failing pairing policy tests**

Test six-digit zero-padded PINs, five-minute default expiry, explicit close, wrong/expired indistinguishability, successful issue with label, always-on rotation every five minutes, per-source and global lockouts, recovery after the rate window, no managed-token secret in status, and PIN visibility only in admin status.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/auth/ -run 'TestPairing' -count=1
```

Expected: missing pairing manager.

- [ ] **Step 3: Implement pairing manager**

Provide:

```go
type PairingOption func(*Pairing)
type PairingStatus struct { Open bool; ExpiresAt time.Time; PIN string `json:"pin,omitempty"` }
func NewPairing(tokens *Store, ttl time.Duration, alwaysOn bool, opts ...PairingOption) *Pairing
func (p *Pairing) Open() PairingStatus
func (p *Pairing) Status(admin bool) PairingStatus
func (p *Pairing) Close()
func (p *Pairing) Exchange(source, pin, label string) (secret string, meta TokenMeta, err error)
```

Use `crypto/rand` rejection sampling for unbiased six-digit PINs. `PairingStatus.PIN` uses `json:"pin,omitempty"`; return it only from authenticated admin status, never from general device state or logs.

- [ ] **Step 4: Verify green**

```bash
go test -race ./internal/auth/ -count=1
```

Expected: token and pairing tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/pairing.go internal/auth/pairing_test.go
git commit -m "Add PIN-based API client enrollment"
```

### Task 12: Replace single-token middleware and expose admin APIs

**Files:**
- Create: `internal/api/admin.go`
- Create: `internal/api/admin_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `internal/api/pairing.go`
- Modify: `internal/api/pairing_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: auth store/pairing manager and existing BLE pairing manager.
- Produces: role-aware middleware, `/pair`, pairing-mode, token, and settings routes while keeping BLE pairing distinct.

- [ ] **Step 1: Write failing auth/administration route tables**

Test bootstrap and managed tokens on client routes, managed-token rejection on admin/advanced routes, unauthenticated `/pair`, wrong/expired PIN 401, source-IP rate limiting, one-time secret response, pairing-mode GET/POST/DELETE, authenticated PNG QR output encoding the exact current `wattline://pair` payload, metadata-only token list, bootstrap revoke rejection, managed revoke, and unambiguous BLE-pairing versus API-pairing route names.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/api/ -run 'Test(Auth|ClientPair|PairingMode|Tokens|Admin)' -count=1
```

Expected: current middleware rejects managed tokens and routes are missing.

- [ ] **Step 3: Implement role-aware middleware and handlers**

Register:

```go
mux.HandleFunc("POST /api/v1/pair", s.clientPair)
mux.HandleFunc("GET /api/v1/pairing-mode", s.admin(s.pairingModeStatus))
mux.HandleFunc("POST /api/v1/pairing-mode", s.admin(s.openPairingMode))
mux.HandleFunc("DELETE /api/v1/pairing-mode", s.admin(s.closePairingMode))
mux.HandleFunc("GET /api/v1/pairing-mode/qr.png", s.admin(s.pairingQRCode))
mux.HandleFunc("GET /api/v1/tokens", s.admin(s.listTokens))
mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.admin(s.revokeToken))
mux.HandleFunc("GET /api/v1/settings", s.admin(s.getSettings))
mux.HandleFunc("PUT /api/v1/settings", s.admin(s.putSettings))
```

Put `auth.Principal` in request context. Apply `admin` to running mode, barrier-free, USB firmware, BLE PIN, bypass threshold, OTA, clock, pairing-mode, token, settings, and TLS-rotation endpoints. Keep `/api/v1/pairing/*` as the authenticated router-to-Link-Power BLE flow using `ble_pin`. Generate the PNG with the pinned `github.com/skip2/go-qrcode` module; the image endpoint must set `Content-Type: image/png`, `Cache-Control: no-store`, and must not accept a PIN in its query string. `PUT /settings` persists through an injected `SaveMain` callback and returns `"restart_required":true` when listener, TLS, mDNS, or firewall fields change.

- [ ] **Step 4: Verify green**

```bash
go test ./internal/api/ ./internal/auth/ -count=1
```

Expected: all authentication and pairing tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api go.mod go.sum
git commit -m "Expose client pairing and token administration"
```

### Task 13: Add certificate initialization and dual HTTP/HTTPS servers

**Files:**
- Create: `internal/server/cert.go`
- Create: `internal/server/cert_test.go`
- Create: `internal/server/listeners.go`
- Create: `internal/server/listeners_test.go`
- Modify: `cmd/wattlined/main.go`
- Modify: `cmd/wattlined/main_test.go`

**Interfaces:**
- Consumes: validated listener/TLS config and API handler.
- Produces: idempotent `-init`, ECDSA certificate/fingerprint, explicit IPv4/IPv6 HTTP and HTTPS listeners, and graceful shutdown.

- [ ] **Step 1: Write failing certificate/listener tests**

Test ECDSA P-256 self-signing, DER SHA-256 lowercase fingerprint, key mode `0600`, idempotent init, explicit rotation changing the fingerprint, SANs for router hostname/localhost, HTTP and HTTPS listener matrices, independent disablement, IPv6-only socket behavior that cannot collide with the IPv4 listener, bind errors, and graceful shutdown of all listeners.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/server/ ./cmd/wattlined/ -run 'Test(Cert|Init|Listener|Bind)' -count=1
```

Expected: missing package/flags.

- [ ] **Step 3: Implement initialization and listener group**

Provide:

```go
type Certificate struct { CertFile, KeyFile, SHA256 string }
func EnsureCertificate(certFile, keyFile string, names []string) (Certificate, error)
func RotateCertificate(certFile, keyFile string, names []string) (Certificate, error)
type Endpoint struct { Enabled bool; Addr4, Addr6 string; Port int }
type ListenerConfig struct { HTTP, HTTPS Endpoint; CertFile, KeyFile string }
type Group struct { servers []*http.Server; listeners []net.Listener }
func Start(ctx context.Context, cfg ListenerConfig, handler http.Handler) (*Group, error)
func (g *Group) Shutdown(context.Context) error
```

Add `wattlined -init -config /etc/config/wattline` to generate missing bootstrap token/certificate idempotently. Replace the single `ListenAndServe` in `run` with the listener group. Do not redirect HTTP to HTTPS.

Create IPv4 listeners with network `tcp4`. Create IPv6 listeners with `net.ListenConfig.Control` setting `IPV6_V6ONLY=1` before bind, then network `tcp6`, so `0.0.0.0:8377` and `[::]:8377` can coexist on Linux.

- [ ] **Step 4: Add and test TLS rotation API wiring**

Implement admin `POST /api/v1/tls/rotate` through an injected rotation callback. Return the new fingerprint and require a daemon listener reload/restart path documented in the response. Test admin-only behavior and changed pin.

- [ ] **Step 5: Verify green**

```bash
go test -race ./internal/server/ ./cmd/wattlined/ ./internal/api/ -count=1
```

Expected: all certificate, listener, daemon, and API tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/server cmd/wattlined internal/api
git commit -m "Serve dual-stack HTTP and HTTPS"
```

## Milestone 3 — Discovery and OpenWrt integration

### Task 14: Add LAN-only mDNS and MagicDNS metadata

**Files:**
- Create: `internal/discovery/txt.go`
- Create: `internal/discovery/txt_test.go`
- Create: `internal/discovery/service.go`
- Create: `internal/discovery/service_test.go`
- Create: `internal/discovery/tailscale.go`
- Create: `internal/discovery/tailscale_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `package/Makefile`
- Modify: `cmd/wattlined/main.go`

**Interfaces:**
- Consumes: state identity, daemon version, HTTP/HTTPS configuration, certificate fingerprint, configured LAN interfaces.
- Produces: dynamic `_wattline._tcp` publication and optional MagicDNS name in device/pairing responses.

- [ ] **Step 1: Write failing TXT/interface tests**

Table-test preliminary/no-MAC suppression, exact key ordering/content, four-digit lowercase CID, eight-digit lowercase features, 64-digit lowercase certificate fingerprint, HTTPS-preferred port, HTTP fallback, `tls=none`, only configured existing interfaces, update/re-register behavior, and Tailscale JSON with/without `Self.DNSName`.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/discovery/ -count=1
```

Expected: package missing.

- [ ] **Step 3: Implement pure TXT and MagicDNS helpers**

Provide:

```go
type Metadata struct { Version, ID, Model, TLS string; CID uint16; Features uint32; API int }
func TXT(Metadata) []string
func PreferredPort(config.Config) int
func ParseTailscaleStatus([]byte) string
```

Call `tailscale status --json` only when the executable exists, with a short context timeout, and treat every failure as “name unavailable.” Do not make it a package dependency.

- [ ] **Step 4: Implement responder behind a test seam**

Use `github.com/grandcat/zeroconf@v1.0.0` and an injected registrar interface. Resolve only UCI-configured interfaces (default `br-lan`). Do not fall back to all interfaces. Do not publish until MAC is known. Re-register when identity, features, TLS, listener port, or version changes. Add `var version = "dev"` in `cmd/wattlined` and inject the IPK version with Makefile linker flag `-X main.version=$(VERSION)` so TXT `ver` matches package metadata.

- [ ] **Step 5: Verify green**

```bash
go test -race ./internal/discovery/ ./cmd/wattlined/ -count=1
```

Expected: all discovery and daemon wiring tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/discovery go.mod go.sum cmd/wattlined package/Makefile
git commit -m "Advertise Wattline with LAN mDNS"
```

### Task 15: Provision credentials and synchronize WAN firewall rules

**Files:**
- Modify: `package/wattlined/etc/uci-defaults/99-wattline`
- Modify: `package/wattlined/etc/init.d/wattlined`
- Modify: `package/wattlined/CONTROL/postinst`
- Modify: `package/wattlined/CONTROL/control`
- Create: `package/wattlined/usr/lib/wattline/firewall-sync`
- Create: `package/wattlined/etc/hotplug.d/iface/95-wattline`
- Create: `package/tests/firewall-sync_test.sh`
- Modify: `package/Makefile`
- Modify: `package/check-ipk-metadata.sh`

**Interfaces:**
- Consumes: new UCI defaults and `wattlined -init`.
- Produces: idempotent first boot, procd lifecycle, persistent credential directories, default-off WAN policy, and correctly packaged files.

- [ ] **Step 1: Write failing shell/package tests**

Create a fake `uci`/`logger` harness that asserts disabled WAN removes only `wattline_http`/`wattline_https`, enabled WAN writes correct ports and warning, repeated runs are identical, and unrelated firewall sections survive. Extend metadata checks to require `0600` key/token-store parent permissions where representable and executable helper modes.

- [ ] **Step 2: Verify red**

```bash
sh package/tests/firewall-sync_test.sh
```

Expected: missing helper failure.

- [ ] **Step 3: Implement first-boot and procd integration**

Make `99-wattline` call `/usr/bin/wattlined -init -config /etc/config/wattline`, then set only missing UCI defaults. Make procd run firewall synchronization before starting, declare stdout/stderr/respawn, and restart on listener/TLS changes while retaining SIGHUP rules reload behavior. Bump the package default `VERSION` from `1.2.0` to `1.3.0` and ensure the staged IPK copies `wattlined/usr/lib` and hotplug files with executable modes.

- [ ] **Step 4: Implement firewall synchronization**

The helper must manage named UCI firewall sections only, use configured enabled ports, default to no WAN accepts, log exactly `wattline: WAN access enabled: insecure — use TLS/VPN`, commit only when content changed, and reload firewall after a change. The hotplug script reruns synchronization on relevant interface-up events.

- [ ] **Step 5: Build and inspect packages**

```bash
make -C package clean all
package/check-ipk-metadata.sh package/out/*.ipk
```

Expected: four IPKs build, metadata passes, and wattlined IPK contains init/default/firewall/hotplug files with safe modes.

- [ ] **Step 6: Commit**

```bash
git add package
git commit -m "Provision TLS and WAN firewall policy"
```

## Milestone 4 — Router UIs, final contract, and verification

### Task 16: Extend LuCI for identity, client pairing, tokens, and reachability

**Files:**
- Modify: `package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js`
- Modify: `package/luci-app-wattline/www/luci-static/resources/view/wattline/settings.js`
- Modify: `package/luci-app-wattline/usr/share/rpcd/acl.d/luci-app-wattline.json`
- Create: `package/tests/luci_contract_test.sh`

**Interfaces:**
- Consumes: canonical API, admin bootstrap token via UCI, new settings keys, QR URI returned by pairing-mode status.
- Produces: LuCI controls required by the design without exposing bootstrap secrets in rendered QR data.

- [ ] **Step 1: Write failing static contract test**

Assert the LuCI sources reference canonical routes, separate labels “Pair Link-Power over BLE” and “Pair an API client,” render identity/fingerprint/MagicDNS, list/revoke tokens, expose HTTP/HTTPS/mDNS/advanced/pairing/WAN settings, and contain confirmation text for destructive/security actions.

- [ ] **Step 2: Verify red**

```bash
sh package/tests/luci_contract_test.sh
```

Expected: required strings/routes missing.

- [ ] **Step 3: Implement status and enrollment UI**

Update the fetch helper to choose HTTPS port when enabled, parse canonical JSON errors, and retain HTTP fallback. Add identity/capability and pending-command displays. Keep BLE pairing PIN state separate from client-enrollment PIN. Fetch `/api/v1/pairing-mode/qr.png` with the admin bearer token, convert the response to an object URL for `<img>`, revoke old object URLs, and never embed the bootstrap token in the QR payload.

- [ ] **Step 4: Implement settings and token UI**

Add UCI form options with the approved defaults/warnings. Add token metadata/revoke actions and enrollment TTL countdown. Require confirmation for WAN, pairing-always-on, certificate rotation, OTA, BLE PIN, shutdown, and factory running mode.

- [ ] **Step 5: Verify**

```bash
sh package/tests/luci_contract_test.sh
make -C package ipk-luci
```

Expected: static contract passes and LuCI IPK builds.

- [ ] **Step 6: Commit**

```bash
git add package/luci-app-wattline package/tests/luci_contract_test.sh
git commit -m "Extend LuCI Wattline administration"
```

### Task 17: Extend the GL panel with the same administration surface

**Files:**
- Modify: `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`
- Modify: `package/gl-app-wattline/usr/lib/oui-httpd/rpc/wattline`
- Create: `package/tests/gl_contract_test.sh`

**Interfaces:**
- Consumes: canonical API and GL authenticated RPC for non-secret listener configuration/bootstrap access.
- Produces: GL oui parity with LuCI for identity, pairing, tokens, reachability, TLS, and controls.

- [ ] **Step 1: Write failing GL static contract test**

Assert the Vue source contains all canonical route paths, separate BLE/API pairing labels, identity and pending state, token revoke, fingerprint/MagicDNS, settings/warnings, and no QR construction from the bootstrap token.

- [ ] **Step 2: Verify red**

```bash
sh package/tests/gl_contract_test.sh
```

Expected: required strings/routes missing.

- [ ] **Step 3: Extend RPC and Vue state/actions**

Return HTTP/HTTPS enablement and ports from RPC without returning private key material. Prefer HTTPS API calls, parse canonical errors, preserve existing two-second telemetry polling behavior, and avoid rebuilding focused input cards on every poll.

- [ ] **Step 4: Add panels and destructive confirmations**

Match LuCI capabilities and wording. Keep client pairing, BLE pairing, token management, and reachability cards separate. Hide unsupported controls and show advanced controls only when both supported and enabled.

- [ ] **Step 5: Verify gzip packaging**

```bash
sh package/tests/gl_contract_test.sh
make -C package ipk-glapp
```

Expected: static contract passes and GL IPK includes the gzipped Vue bundle.

- [ ] **Step 6: Commit**

```bash
git add package/gl-app-wattline package/tests/gl_contract_test.sh
git commit -m "Extend GL Wattline administration"
```

### Task 18: Lock documentation coverage and run final verification

**Files:**
- Modify: `docs/api.md`
- Create: `internal/api/contract_test.go`
- Modify: `README.md`
- Create: `docs/gl-x3000-verification.md`

**Interfaces:**
- Consumes: final registered routes, JSON structs, UCI/package behavior, and all test evidence.
- Produces: deliverable-of-record contract, route coverage guard, build/install documentation, and real-hardware checklist.

- [ ] **Step 1: Write the failing route/document coverage test**

Define one exported/internal route descriptor table used by `NewServer` and test that every descriptor's method/path appears verbatim in `docs/api.md`. Also assert required contract phrases for auth roles, error envelope, SSE framing, mDNS TXT, pairing URI, TLS fingerprint, and every compatibility alias.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/api/ -run TestContractDocumentation -count=1
```

Expected: any undocumented/mismatched routes are listed.

- [ ] **Step 3: Complete the contract and README**

Resolve every test-reported omission in `docs/api.md`. Update README configuration, HTTP/HTTPS examples, pairing/token flow, mDNS, MagicDNS, firewall warning, package filenames/versioning, gzip-tar/ustar rules, force-reinstall instructions, and explicit no-OTA-flashing statement.

- [ ] **Step 4: Write the on-target checklist**

In `docs/gl-x3000-verification.md`, include commands and expected observations for package install/reboot, BLE handshake identity, every control frame, telemetry reconciliation, clock availability, timer CRUD, restart/shutdown/OTA lifecycle, advanced controls, HTTP/HTTPS IPv4/IPv6, certificate pinning, mDNS LAN-only scope/TXT, Tailscale MagicDNS, optional WireGuard, WAN default deny/explicit allow, client pairing/token revoke, both UIs, rules/webhooks/SSE, and persistence across reboot. Mark each checkbox `NOT RUN — requires GL-X3000/real BLE` until actually exercised.

- [ ] **Step 5: Run the full uncached Go suite**

```bash
go test -count=1 ./...
```

Expected: every package passes with no failures.

- [ ] **Step 6: Run race tests on concurrency-sensitive packages**

```bash
go test -race -count=1 ./internal/state/ ./internal/ble/ ./internal/control/ ./internal/auth/ ./internal/api/ ./internal/server/ ./internal/discovery/
```

Expected: all pass with no race reports.

- [ ] **Step 7: Build all OpenWrt packages and verify archives**

```bash
make -C package clean all
package/check-ipk-metadata.sh package/out/*.ipk
```

Expected: wattlined, wattline-bt, luci-app-wattline, and gl-app-wattline IPKs build and every archive passes metadata checks.

- [ ] **Step 8: Review scope and diff**

```bash
git diff --check
git status --short
git diff --stat origin/main...HEAD
rg -n 'TO[D]O|TB[D]|FIXM[E]' docs/api.md docs/gl-x3000-verification.md internal package README.md
```

Expected: no whitespace errors, no unintended files, no unresolved contract gaps, and no Swift paths.

- [ ] **Step 9: Commit final contract and verification material**

```bash
git add docs/api.md docs/gl-x3000-verification.md internal/api/contract_test.go README.md
git commit -m "Finalize Wattline API contract and verification"
```

- [ ] **Step 10: Produce delivery evidence**

Record the exact commit range, `go test ./...` output, `make -C package all` output, IPK filenames, and the checklist items still requiring GL-X3000/real BLE in the final handoff. Do not mark hardware checks complete without fresh on-target evidence.
