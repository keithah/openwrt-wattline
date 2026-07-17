# Wattlined Full Control API Design

**Date:** 2026-07-17

**Status:** Approved in conversation; awaiting review of this written specification

## Goal

Extend the existing OpenWrt `wattlined` daemon into the complete, versioned HTTP control plane for a PeakDo Link-Power device while preserving the existing rules, webhooks, telemetry, SSE, UCI configuration, BLE pairing, and legacy HTTP routes.

The router daemon is the only implementation in scope. No Swift application or other off-router client is modified.

## Sources of truth

- `docs/API.md` is the read-only Link-Power BLE protocol reference. It explicitly does not describe a REST API.
- `docs/api.md` will be the authoritative daemon HTTP API contract consumed by future iOS and macOS clients.
- The current daemon routes in `internal/api/server.go` define the compatibility surface that must remain available.
- The existing `internal/proto`, `internal/ble`, `internal/state`, `internal/rules`, and `internal/actions` implementations remain authoritative and are extended rather than forked.

## Delivery sequence

Work is delivered as four sequential, independently testable milestones, but the final product is considered complete only when all four are present:

1. BLE protocol parity and the canonical device API.
2. Client pairing, managed tokens, TLS, and reachability policy.
3. mDNS, OpenWrt packaging, provisioning, and firewall integration.
4. LuCI/GL UI, the complete `docs/api.md` contract, and full verification.

## Architecture

The implementation uses one integrated daemon. HTTP handlers call a control service backed by the existing `ble.Session`; all command frames use the existing protocol codec and transport. BLE notifications update the existing state store, and the state store drives HTTP reads, reconciliation, rules, and SSE.

```text
HTTP handlers
    -> control service
    -> ble.Session transaction mutex
    -> internal/proto encoders and decoders
    -> existing BLE Transport

BLE notifications
    -> state.Store
    -> pending-command reconciliation
    -> status, telemetry, rules, and SSE
```

New responsibilities are isolated as follows:

- `internal/proto`: exact frame encoders and reply parsers only.
- `internal/ble`: characteristic availability, serialized command sequences, clock and OTA operations, disconnect-as-success semantics, and reconnect policy.
- `internal/state`: identity, connection phase, decoded capabilities, and pending/confirmed/timeout command state in addition to existing telemetry and history.
- `internal/api`: canonical resource routes, legacy aliases, JSON errors, role enforcement, and contract serialization.
- `internal/auth`: client token hashing and persistence, pairing-mode PIN lifecycle, rate limiting, revocation, and last-seen coalescing.
- `internal/discovery`: an in-process DNS-SD responder and dynamic TXT generation.
- `internal/server`: coordinated HTTP and HTTPS listeners on IPv4 and IPv6.

The rules engine, webhook executor, pairing manager, BLE transports, UCI parser/serializer, and SSE subscription mechanism are reused.

Startup order is: load configuration; load or initialize credentials; start HTTP and HTTPS listeners; publish discovery only when the configured or handshaken Link-Power MAC is known; connect and handshake BLE; enrich identity and discovery data; continue the existing rules, history, and SSE behavior. The API remains online while BLE is disconnected.

## Protocol and BLE behavior

Every command-channel transaction remains write-then-read through one `ble.Session` mutex. Multi-command operations such as SET-then-GET and mutate-then-list hold the same transaction ownership across the complete sequence so another request cannot interleave.

The following protocol requirements are exact:

- DC output uses `[0x01,0x01,op]` and confirms only from `DcPortStatus.enabled`.
- USB-C output uses `[0x13,0x01,0x02,op]` and confirms only from `TypeCPortStatus.mode`: mode 3 to 1 for off and mode 1 to 3 for on. `enabled` is not used for confirmation.
- USB-C limits use command `0x02`, types 1 through 4, and levels 0 through 5 for 30, 45, 60, 65, 100, and 140 W. PUT performs SET then re-GET and returns the observed level. DELETE sends `[0x02,0x02,type]`, never opcode `0x06`, then re-GETs. Runtime is read-only, and `0xFF` means unset only for runtime.
- DC bypass sends `[0x14,0x01,op]`, ignores the reply result byte, and confirms only from `DcPortStatus.bypassOn` within ten seconds.
- DC bypass threshold uses command `0x15` with little-endian BLE SFLOAT volts.
- Restart sends command `0x11`; shutdown writes `FM` to `0x4310`; OTA entry writes `PK` to `0x4301`. A disconnect, including a write error while disconnecting, is success. Restart arms automatic reconnect after approximately fifteen seconds. Shutdown disarms reconnect. OTA entry reconnects in bootloader mode.
- OTA support is limited to enter, exit, and read-only INFO. Firmware erase, program, verify, download, and flashing are out of scope.
- Clock sync writes the exact ten-byte Current Time structure with adjustment reason `0`; the connection handshake continues using reason `1`.
- Clock GET reads `0x2A2B` only when characteristic discovery recorded it as readable. If absent, it performs zero transport I/O and reports drift unavailable.
- Timers use command `0x06` and the exact nine-byte `TIMER_SETTINGS` body. POST adds with ID `0xFF` and adopts the reply ID. PUT edits the URL ID. All mutations re-list and return authoritative device state. Status values `1` and `-1` are writable; `-2` and `-3` are rendered device states.
- Advanced controls cover running mode `0xE0`, barrier-free mode `0x03`, USB firmware version `0x17`, and SET-only BLE PIN `0x04` as unsigned 32-bit little-endian. Opcodes `0x05`, `0x12`, and `0xF0` are not implemented.
- OTA bootloader mode is a valid session state exposing identity, OTA INFO, and exit. It is not treated as a fatal handshake error.

Identity includes model, hardware revision/variant, application firmware, OTA bootloader firmware, reversed device MAC, CID, raw FEATURES, decoded feature booleans, characteristic-derived availability, app/OTA mode, connection/reconnect state, and command state.

## Command reconciliation

A mutation creates a command record before BLE I/O and immediately updates SSE-visible state. Each record contains a stable command ID, operation, requested state, phase, start time, last update time, and terminal error when present. State retains every active record and a bounded ring of the 32 most recent terminal records.

Phases are `pending`, `confirmed`, `timeout`, and `failed`.

- DC, USB-C output, and bypass wait for their specified telemetry field without holding the low-level command mutex after the command transaction finishes.
- A confirmed mutation returns HTTP 200 with observed device state.
- A telemetry timeout returns HTTP 504 with code `command_timeout`; the timed-out record remains visible.
- BLE write/read failures return HTTP 502 and transition the record to `failed`.
- USB limit and timer operations return only re-read device state, never an optimistic request echo.
- Restart, shutdown, and OTA disconnect-as-success operations transition to confirmed when the expected disconnect is observed or when the write is already failing due to that disconnect.

## Canonical HTTP API

Canonical version-one resources are:

| Resource | Routes |
|---|---|
| Device identity/state | `GET /api/v1/device` |
| DC output | `POST /api/v1/device/dc` |
| USB-C output | `POST /api/v1/device/usbc/output` |
| USB-C limits | `GET`, `PUT`, `DELETE /api/v1/device/usbc/limit/{global|input|output|runtime}` |
| DC bypass | `POST /api/v1/device/dc/bypass` |
| Bypass threshold | `GET`, `PUT /api/v1/device/dc/bypass/threshold` |
| Power lifecycle | `POST /api/v1/device/restart`, `POST /api/v1/device/shutdown` |
| OTA mode | `GET /api/v1/device/ota`, `POST /api/v1/device/ota/enter`, `POST /api/v1/device/ota/exit` |
| Clock | `GET /api/v1/device/clock`, `POST /api/v1/device/clock/sync` |
| Timers | `GET`, `POST /api/v1/device/timers`; `GET`, `PUT`, `DELETE /api/v1/device/timers/{id}` |
| Running mode | `PUT /api/v1/device/advanced/running-mode` |
| Barrier-free mode | `GET`, `PUT /api/v1/device/advanced/barrier-free` |
| USB firmware | `GET /api/v1/device/advanced/usb-fw-version` |
| BLE PIN | `PUT /api/v1/device/advanced/ble-pin` |
| API-client enrollment | `POST /api/v1/pair` |
| Pairing mode | `GET`, `POST`, `DELETE /api/v1/pairing-mode`; `GET /api/v1/pairing-mode/qr.png` |
| Managed tokens | `GET /api/v1/tokens`, `DELETE /api/v1/tokens/{id}` |
| Daemon settings | `GET`, `PUT /api/v1/settings` |
| TLS certificate | `POST /api/v1/tls/rotate` |

Existing status, telemetry, history, events, rules, action, BLE-pairing, USB-C-limit, bypass-threshold, and schedule routes remain available. Their current successful response shapes remain compatible and `docs/api.md` marks them deprecated where canonical equivalents exist.

Canonical errors use this shape:

```json
{
  "error": {
    "code": "device_disconnected",
    "message": "Link-Power is not connected",
    "details": {}
  }
}
```

Relevant policy statuses are:

- `400 invalid_request` for malformed JSON, ranges, or timer structure.
- `401 unauthorized` for absent or invalid bearer credentials.
- `401 invalid_or_expired_pin` for client enrollment failure.
- `403 admin_required` for client tokens accessing administration.
- `403 advanced_disabled` when policy disables an otherwise supported advanced operation.
- `404 not_found` for missing timer/token resources.
- `409 capability_unsupported` when hardware or mode lacks an operation.
- `503 device_disconnected` when live BLE is required.
- `502 ble_operation_failed` for command or characteristic failures.
- `504 command_timeout` for telemetry reconciliation timeout.

## SSE compatibility

`GET /api/v1/events` remains `text/event-stream`. It sends an initial snapshot immediately and subsequent complete snapshots as unnamed events:

```text
data: {JSON snapshot}\n
\n
```

The snapshot is extended with identity, capability, connection, and command-state fields. It retains existing telemetry fields and does not wrap them in a new envelope, preserving clients that use `EventSource.onmessage`.

## BLE pairing and API-client pairing

Two PIN concepts are kept separate everywhere:

- `ble_pin` is the Link-Power bond PIN, currently defaulting to `020555`, stored in UCI and used only by router-to-device BLE pairing.
- `pairing_pin` is a random, short-lived daemon enrollment PIN used only by `POST /api/v1/pair`.

The existing authenticated BLE scan/pair/unpair endpoints remain in place. The new unauthenticated client-enrollment endpoint accepts `{pin,label}` and returns a new per-client API token once.

Admin-only pairing-mode operations open, inspect, and close enrollment. Opening pairing mode generates a random six-digit PIN with a default five-minute TTL. `pairing_always_on=1` keeps enrollment available while continuing to rotate the PIN every five minutes. Per-source and global failure limits mitigate brute-force attempts.

The QR payload is a versioned URI:

```text
wattline://pair?v=1&id=DEVICE_ID&host=PREFERRED_HOST&http=8377&https=8378&pin=123456&tls=CERT_SHA256
```

It contains the enrollment PIN, never the bootstrap token. The exact escaping, omitted-field rules, and response JSON are defined in `docs/api.md`.

## Token policy and persistence

The existing UCI `token` remains the bootstrap/admin credential. It appears in token listings as ID `bootstrap`, is never returned by the API, and cannot be revoked through the API.

Client tokens are random 256-bit values. The API returns a new secret only once. Persistent storage defaults to `/etc/wattline/tokens.json`, is UCI-configurable, and is a mode-`0600` JSON file containing token ID, SHA-256 secret hash, label, creation time, and last-seen time. Last-seen writes are coalesced to limit flash wear.

Client tokens may read telemetry/state/history/rules, consume SSE, and control the Link-Power. Only the bootstrap/admin token may open pairing mode, list or revoke tokens, change listener/TLS/firewall policy, rotate certificates, or call advanced endpoints.

`GET /api/v1/tokens` returns metadata only. `DELETE /api/v1/tokens/{id}` revokes a managed token immediately. Authentication comparison is constant-time.

## Advanced policy

UCI `advanced` defaults to `0`. Device identity reports hardware support separately from administrative enablement. An advanced endpoint returns `409 capability_unsupported` when unavailable in hardware or the current mode and `403 advanced_disabled` when supported but disabled by policy.

The advanced policy gates running mode, barrier-free mode, USB firmware version, BLE-PIN changes, bypass threshold, OTA enter/exit/info, and clock reads.

## HTTP, HTTPS, and certificates

- HTTP defaults to port `8377` and remains enabled for compatibility and encrypted VPN use.
- HTTPS defaults to port `8378`.
- Both explicitly bind IPv4 `0.0.0.0` and IPv6 `[::]` by default.
- UCI can independently change addresses, ports, and enablement without removing existing keys.
- First-boot initialization generates an ECDSA P-256 self-signed certificate at `/etc/wattline/tls/server.crt`, its mode-`0600` key at `/etc/wattline/tls/server.key`, and the bootstrap token without an OpenSSL runtime dependency. Paths remain UCI-configurable.
- The SHA-256 fingerprint is computed over the DER certificate and rendered as a lowercase hexadecimal value in the API, QR payload, UI, and mDNS TXT record.
- Pairing responses contain HTTPS and HTTP base URLs, the fingerprint, device ID, and MagicDNS name when available.
- Certificate rotation is an explicit admin operation because clients must update their pin.
- Plain HTTP over Tailscale or WireGuard is documented as tunnel-encrypted. Plain HTTP over WAN is explicitly insecure.

## Discovery and remote identity

The daemon uses an in-process Go zeroconf responder instead of Avahi. This avoids another daemon and package dependency and allows TXT data to update atomically when BLE identity or TLS state changes.

`_wattline._tcp` is published only on configured LAN interfaces, defaulting to `br-lan`. The advertised port is HTTPS when enabled and HTTP otherwise. TXT keys are exactly:

- `ver`: daemon/package version.
- `api`: HTTP API version, initially `1`.
- `id`: stable Link-Power MAC. The service is not published until this is available from UCI or a completed handshake.
- `model`: model string or empty while disconnected/unknown.
- `cid`: exactly four lowercase hexadecimal digits without `0x` (for example `0305`), or empty while unknown.
- `features`: eight-digit lowercase hexadecimal raw feature mask, or empty while unknown.
- `tls`: lowercase certificate SHA-256 fingerprint, or `none` when HTTPS is disabled.
- `auth`: `pin`.

mDNS remains LAN-only. The same `id` allows clients to correlate LAN discovery with saved remote endpoints.

The daemon opportunistically reads the local Tailscale client's MagicDNS name without introducing a Tailscale package dependency. The name is exposed in device and pairing responses. WireGuard and other VPN reachability uses the same all-interface listeners and the router's configured firewall-zone policy.

## Firewall policy

`wan_access` defaults to `0`. Packaging installs an idempotent firewall synchronization helper. When disabled, Wattline adds no WAN acceptance rule. When enabled, it creates named WAN rules for the configured HTTP and HTTPS ports and logs: `insecure — use TLS/VPN`.

The helper updates only Wattline-owned rules and does not alter unrelated firewall state. The UI distinguishes listener state from actual firewall reachability. Tailscale and WireGuard verification includes confirming their existing zone/policy allows the configured ports; arbitrary VPN firewall zones are not silently rewritten.

## LuCI and GL panel behavior

Both router UIs provide the same capabilities while following their existing framework conventions:

- Complete device identity, mode, connection, firmware, CID, and decoded capabilities.
- Existing telemetry, rules, power controls, limits, bypass threshold, timers, restart, and shutdown.
- A clearly named “Pair Link-Power over BLE” flow using `ble_pin`.
- A separate “Pair an API client” flow showing enrollment PIN, expiry, QR, addresses, TLS fingerprint, and MagicDNS name.
- Managed token metadata and revoke controls.
- HTTP/HTTPS address and port settings, mDNS LAN interface, advanced policy, pairing-always-on, and WAN access.
- Prominent confirmation/warning text for shutdown, OTA entry, BLE-PIN changes, certificate rotation, pairing-always-on, and WAN access.
- Advanced device controls shown only when supported and enabled.

The UIs use the bootstrap token through their existing authenticated router RPC/UCI access. They prefer HTTPS when enabled and use the canonical routes. They do not expose the bootstrap secret in QR codes.

## UCI and packaging

Existing keys retain their meanings. New keys cover BLE PIN naming compatibility, listener addresses and enablement, HTTPS port and certificate paths, managed-token path, pairing TTL/always-on policy, advanced policy, mDNS enablement/interfaces, and WAN access.

The package continues to produce gzip-compressed tar IPKs with ustar archives, normalized root ownership/modes, version injection into controls/filenames/feed metadata, force-reinstall compatibility, and architecture `aarch64_cortex-a53`.

The `wattlined` package gains the zeroconf Go dependency within the static binary, first-boot initialization, certificate/token directories, firewall synchronization files, and updated procd behavior. It does not depend on Avahi or OpenSSL.

## Testing strategy

Implementation follows strict red-green-refactor TDD.

- Every new protocol encoder/parser has table tests with exact hexadecimal fixtures from `docs/API.md`.
- Every canonical route tests successful JSON, method/path behavior, input validation, authorization role, disconnection, unsupported capability, advanced policy, BLE failures, and timeouts where relevant.
- Reconciliation tests prove DC uses `enabled`, USB-C output uses `mode`, bypass ignores result bytes, and SSE observes pending then terminal state.
- USB-limit tests prove DELETE uses opcode `0x02`, mutations re-GET, runtime is read-only, and only runtime accepts `0xFF` unset.
- Timer tests cover all four recurrence layouts, writable/render-only status values, assigned IDs, mutation re-listing, and malformed dates/masks.
- Clock tests prove absent characteristics cause zero transport calls.
- Lifecycle tests cover disconnect-as-success, restart reconnect arming, shutdown disarming, OTA app/bootloader transitions, and no flashing routes.
- Auth tests cover hashing, one-time secret return, TTL, always-on rotation, rate limits, roles, revocation, constant-time lookup behavior, metadata-only listing, and throttled last-seen persistence.
- Network policy tests cover default listeners, independent disablement, TLS fingerprinting, certificate initialization/rotation, mDNS TXT and interface selection, Tailscale-name detection fallback, and default-off WAN rules.
- Contract tests compare registered routes and documented routes and lock the SSE snapshot shape.
- Existing rules, webhook, state, pairing, UCI, and compatibility endpoint tests remain green.

Final local verification commands are uncached `go test ./...`, `make -C package all`, `package/check-ipk-metadata.sh package/out/*.ipk`, and `git diff --check`.

## On-target verification

The following require a GL-X3000, its actual firewall/Tailscale configuration, or a real Link-Power and cannot be proven solely by unit tests:

- Exact command and reply frames for every new control.
- DC, USB-C mode, and bypass telemetry reconciliation and timeout behavior.
- Current Time characteristic readability and drift reporting.
- Timer CRUD persistence and device-rendered `-2`/`-3` states.
- Restart reconnect timing, shutdown reconnect disarming, OTA entry, bootloader INFO, and OTA exit.
- Running mode, barrier-free, USB firmware version, and BLE-PIN change behavior.
- HTTP and HTTPS on IPv4/IPv6 LAN, certificate pinning, and reboot persistence.
- `_wattline._tcp` visibility only on LAN and correct dynamic TXT data.
- Tailscale MagicDNS reachability and correlation with mDNS `id`.
- WireGuard/VPN reachability when such an interface is configured.
- WAN rejection by default and explicit WAN access with the warning enabled.
- LuCI and GL panel BLE pairing, API-client enrollment/QR, token revocation, settings, and all device controls.

## Explicit non-goals

- No Swift or other client application changes.
- No cloud service and no telemetry off the router.
- No OTA firmware download, erase, program, verify, or flashing.
- No implementation of rejected opcodes `0x05`, `0x12`, or `0xF0`.
- No replacement or fork of the existing rules, webhook, state, protocol, or BLE subsystems.
