# Wattlined HTTP API v1

This document is the authoritative HTTP contract for `wattlined`. The uppercase
`docs/API.md` describes the Link-Power BLE protocol and is not an HTTP API.

All JSON examples are normative. Unless an endpoint explicitly says otherwise,
requests and replies use `application/json`, unknown request fields and trailing
JSON values are rejected, timestamps are RFC 3339 UTC strings, and an omitted
request body means zero bytes (not `{}`). Boolean control mutations return the
device-observed value, not an optimistic echo. JSON examples omit the final LF
that Go's JSON encoder appends to every JSON response body.

On registered, non-`OPTIONS` API routes, request bodies are limited to 64 KiB.
Protected routes authenticate before applying that limit, so missing/invalid
credentials still return `401` and a managed token on an admin route still
returns `403`; an oversized authenticated or public request returns `400
invalid_request`. Every registered `GET` and `DELETE` route requires an exactly
zero-byte body; after authentication/authorization, any nonempty body, including
an unknown-length chunked body, returns `400 invalid_request`. Body-limit,
empty-body, authentication, and role errors are global and are omitted from
endpoint-specific error lists and “Additional errors” table columns unless
explicitly repeated. CORS preflight `OPTIONS` requests bypass body validation
and return `204`; unmatched routes use the HTTP mux response and are outside this
API contract. Each listener has a 15-second request-read deadline. There is no
server-wide response write timeout because SSE streams are intentionally
long-lived; each individual SSE frame must write and flush within 10 seconds or
that stream is terminated.

## Versioning and base URLs

The only API version is `v1`; every route starts with `/api/v1`. Breaking wire
changes require a new path version. Additive object fields may appear in v1, so
clients must ignore unknown reply fields.

Default bases are `http://ROUTER:8377/api/v1` and
`https://ROUTER:8378/api/v1`. HTTP and HTTPS can be enabled and bound
independently. Both default to IPv4 `0.0.0.0` and IPv6 `[::]`. HTTP is retained
for compatibility and encrypted VPNs; HTTP exposed directly to WAN is insecure.
No HTTP-to-HTTPS redirect is implied.

OpenWrt packaging keeps `wan_access=0` by default. Netifd-managed WireGuard
interfaces rely on their configured firewall zones. On GL firmware, a late fw3
reload can remove an enabled Tailscale daemon's dynamic `ts-*` chains while
leaving `tailscale0` present; Wattline's lifecycle/hotplug repair detects a
missing `ts-input` chain or INPUT jump and restores VPN reachability by toggling
only Tailscale's netfilter mode from `nodivert` back to `on`.
It does not restart the daemon or act when Tailscale is absent, disabled, or
intact.

Every non-SSE JSON success has the status and body stated below. CORS preflight
`OPTIONS` bypasses bearer authentication and always returns `204 No Content`, an
empty body, and exactly these headers (for every path, registered or not):

```text
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type
Access-Control-Max-Age: 600
```

Preflight performs no BLE I/O. Actual requests still require bearer
authentication except public `POST /api/v1/pair`; successful and error responses
also carry the four `Access-Control-*` headers above.

## Authentication roles

Except `POST /api/v1/pair`, every route requires:

```text
Authorization: Bearer TOKEN
```

Roles are:

- **public**: only the PIN exchange route; no bearer token is accepted or needed.
- **client**: a managed token. It may read state, telemetry, history, rules and
  events and may use ordinary device controls and BLE-device pairing.
- **admin**: the UCI bootstrap token. It has client permissions plus advanced
  controls, rule mutation/manual webhooks, pairing-mode, token, settings, and
  TLS administration. The bootstrap secret is never returned and its token ID
  is `bootstrap`.

Absent, malformed, invalid, or revoked bearer credentials return
`401 unauthorized`. A valid client token on an admin route returns
`403 admin_required`. Authentication checks use constant-time secret comparison.

## Error envelope

Canonical routes return errors only in this shape:

```json
{"error":{"code":"device_disconnected","message":"Link-Power is not connected","details":{}}}
```

`details` is always an object, possibly empty. Stable policy mappings are:

| Status | Code | Meaning |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, unknown/trailing fields, bad range, path value, or timer structure. |
| 401 | `unauthorized` | Bearer credential is absent or invalid. |
| 401 | `invalid_or_expired_pin` | Enrollment PIN is wrong, expired, closed, or rate-limited; those cases are deliberately indistinguishable. |
| 403 | `admin_required` | A client token called an admin route. |
| 403 | `advanced_disabled` | Hardware supports the operation but UCI `advanced=0`. |
| 404 | `not_found` | The requested timer or managed token does not exist. |
| 409 | `capability_unsupported` | Hardware, characteristic inventory, or current app/OTA mode lacks the operation. |
| 409 | `operation_in_progress` | A BLE pairing scan or pair operation is already active. |
| 502 | `ble_operation_failed` | A required BLE command, read, or write failed. |
| 503 | `device_disconnected` | Live BLE is required and unavailable. |
| 504 | `command_timeout` | Telemetry did not confirm a reconciled command in time. |
| 500 | `internal_error` | An internal persistence, certificate, or streaming failure occurred. |

The exact error bodies referred to below are:

| Symbol | Exact JSON body |
|---|---|
| `E(unauthorized)` | `{"error":{"code":"unauthorized","message":"Bearer token is missing or invalid","details":{}}}` |
| `E(invalid_request)` | `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}` |
| `E(admin_required)` | `{"error":{"code":"admin_required","message":"Administrator token required","details":{}}}` |
| `E(advanced_disabled)` | `{"error":{"code":"advanced_disabled","message":"Advanced operations are disabled","details":{}}}` |
| `E(not_found)` | `{"error":{"code":"not_found","message":"Resource was not found","details":{}}}` |
| `E(capability_unsupported)` | `{"error":{"code":"capability_unsupported","message":"Operation is not supported","details":{}}}` |
| `E(operation_in_progress)` | `{"error":{"code":"operation_in_progress","message":"Pairing operation already in progress","details":{}}}` |
| `E(ble_operation_failed)` | `{"error":{"code":"ble_operation_failed","message":"BLE operation failed","details":{}}}` |
| `E(device_disconnected)` | `{"error":{"code":"device_disconnected","message":"Link-Power is not connected","details":{}}}` |
| `E(command_timeout)` | `{"error":{"code":"command_timeout","message":"Device telemetry did not confirm the command","details":{}}}` |
| `E(invalid_or_expired_pin)` | `{"error":{"code":"invalid_or_expired_pin","message":"Pairing PIN is invalid or expired","details":{}}}` |
| `E(internal_error)` | `{"error":{"code":"internal_error","message":"Internal server error","details":{}}}` |

Every listed `E(code)` is returned with `Content-Type: application/json`; the
table is normative and is the exact body, including empty `details`. Method
mismatch and an unregistered path use Go HTTP routing responses and are not
canonical JSON. Compatibility routes retain successful response shapes, while
their errors use this canonical catalog.

## Device identity

### `GET /api/v1/device`

Role: client. Request: none. Success: `200 OK`:

```json
{
  "id":"DC:04:5A:EB:72:2B",
  "model":"BP4SL3V2",
  "hardware_revision":"V2",
  "application_firmware":"1.4.9",
  "ota_firmware":"1.0.3",
  "cid":773,
  "features_raw":32767,
  "features":{"display":true,"factory_mode":true,"sleep":true,"shutdown":true,"battery_capacity":true,"dc_out_port":true,"dc_out_control":true,"dc_out_scheduler":true,"usb_port":true,"usb_power_limit":true,"usb_output_control":true,"dc_bypass":true,"dc_bypass_control":true,"usb_dc_input":true,"usb_dc_input_power":true,"running_mode":true,"barrier_free":true,"usb_firmware":true,"ble_pin":true},
  "available":{"current_time":true,"ota":true,"dc":true,"usbc":true},
  "mode":"app",
  "connection":{"connected":true,"phase":"ready","reconnect":"armed"},
  "commands":{"active":[],"recent":[]},
  "magic_dns_name":"wattline.example.ts.net"
}
```

`id` is the reversed device MAC and is the stable identity used by mDNS and QR.
`mode` is `app` or `ota`; `connection.phase` is `disconnected`, `connecting`,
`handshaking`, `ready`, or `bootloader`; `reconnect` is `armed`, `disarmed`, or
`bootloader`. `features_raw` is the numeric BLE mask while `features` and
`available` are decoded hardware/characteristic facts, independent of the
administrative `advanced` switch.

The first 15 fields in `features` (`display` through `usb_dc_input_power`) map
one-for-one, in bit order 0 through 14, to all 15 documented BLE `FEATURES`
bits. `running_mode`, `barrier_free`, `usb_firmware`, and `ble_pin` are additive
operation-capability conveniences derived from those bits and the discovered
GATT inventory. `available.current_time` means the characteristic exists for
clock sync; clock-read availability is reported authoritatively by
`GET /api/v1/device/clock` because the characteristic may be write-only.

Each command object is exactly:

```json
{"id":"cmd_f00dbabe","operation":"dc_output","requested":{"on":true},"phase":"pending","started_at":"2026-07-17T20:00:00Z","updated_at":"2026-07-17T20:00:00Z","error":null}
```

`phase` is `pending`, `confirmed`, `timeout`, or `failed`. `recent` retains at
most 32 terminal records. Endpoint-specific errors: `400 invalid_request` for a
nonempty body. BLE I/O: none; this is cached state and remains available while
disconnected.

## Telemetry, history, and SSE

### `GET /api/v1/telemetry`

Role: client. Request: none. Success: `200 OK` with the complete cached snapshot:

```json
{"battery":{"enabled":true,"status":1,"full":false,"max_wh":221.0,"wh":170.2,"level":77,"volts":25.6,"amps":1.2,"watts":30.7,"remain_min":332},"dc":{"enabled":true,"status":0,"volts":24.0,"amps":0.5,"watts":12.0,"bypass":false},"typec":{"enabled":true,"status":0,"volts":20.0,"amps":1.0,"watts":20.0,"temp_c":35.0,"mode":3,"dc_input":false},"connected":true,"updated_at":"2026-07-17T20:00:00Z","identity":{"id":"DC:04:5A:EB:72:2B","mode":"app"},"commands":{"active":[],"recent":[]}}
```

Errors: global request-body and authentication errors only. BLE I/O: none.

### `GET /api/v1/history`

Role: client. Request: none. Success: `200 OK` with an array (empty as `[]`):

```json
[{"at":"2026-07-17T19:59:00Z","level":77,"status":1,"dc_w":12.0,"typec_w":20.0}]
```

History is the bounded, approximately one-point-per-minute cache. Errors: global
request-body and authentication errors only. BLE I/O: none.

### `GET /api/v1/events`

Role: client. Request: none. Success: `200 OK`, no JSON response envelope. It
sends one initial complete cached snapshot immediately, then another complete
snapshot on each update. Events are unnamed so `EventSource.onmessage` works.
Each `data:` value uses exactly the complete Snapshot schema defined by
`GET /api/v1/telemetry`: telemetry fields remain top-level and the same
`identity` and `commands` fields are present (including connection/capability
data as that Snapshot schema evolves additively).

```text
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive

data: {complete snapshot JSON}\n
\n
```

Endpoint-specific errors: if streaming is unsupported before response status
`200` and SSE headers are sent, return HTTP `500` with exact body
`E(internal_error)`. Once the `200` stream has begun, a transport failure or
client disconnect ends the stream and cannot carry a JSON error body. Revoking
the managed token that authenticated a live stream ends that stream promptly.
A successful token-store cutover similarly ends streams authenticated by
managed tokens from the old store. Streams authenticated by other current-store
managed tokens or the bootstrap token remain open. Authentication errors occur
before streaming begins. Subscription and capture of the first complete snapshot
are atomic, so an initial frame can never be newer than a subsequently queued
frame. To bound router memory, slow subscribers are disconnected after 128 queued
complete snapshots; a frame that cannot write and flush within 10 seconds also
terminates its stream. Accepted snapshots are delivered in order and are never
silently coalesced. The stream then ends without an SSE error frame, and the client
reconnects to receive a fresh initial snapshot. BLE I/O: none; BLE notifications
update the store independently.

## Granular controls

For reconciled controls, the success body includes the terminal command record.
The write transaction ends before waiting for telemetry. DC confirms only from
`dc.enabled`; USB-C output confirms off at `typec.mode=1` and on at
`typec.mode=3` (never from `typec.enabled`); bypass confirms only from
`dc.bypass` and ignores its BLE result byte.

### `POST /api/v1/device/dc`

Role: client. Request `{"on":true}`. Success `200 OK`:

```json
{"enabled":true,"command":{"id":"cmd_f00dbabe","operation":"dc_output","requested":{"on":true},"phase":"confirmed","started_at":"2026-07-17T20:00:00Z","updated_at":"2026-07-17T20:00:01Z","error":null}}
```

Errors: `400 invalid_request`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`, `504 command_timeout`.
BLE I/O: one serialized command transaction, then telemetry reconciliation.

### `POST /api/v1/device/usbc/output`

Role: client. Request `{"on":false}`. Success `200 OK`:

```json
{"enabled":false,"mode":1,"command":{"id":"cmd_cafebabe","operation":"usbc_output","requested":{"on":false},"phase":"confirmed","started_at":"2026-07-17T20:00:00Z","updated_at":"2026-07-17T20:00:01Z","error":null}}
```

Errors: `400 invalid_request`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`, `504 command_timeout`.
BLE I/O: one serialized command transaction, then telemetry reconciliation.

### `GET /api/v1/device/usbc/limit/{type}`

Role: client. `{type}` is `global`, `input`, `output`, or `runtime`. Request:
none. Success `200 OK`, for example `{"type":"output","level":4,"watts":100}`.
Levels `0..5` map exactly to `30,45,60,65,100,140` watts. Runtime may return
`{"type":"runtime","level":-1,"watts":null}`; only runtime interprets BLE
`0xFF` as unset. Errors: `400 invalid_request`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: one GET command.

### `PUT /api/v1/device/usbc/limit/{type}`

Role: client. `{type}` is `global`, `input`, or `output`; runtime is read-only.
Request `{"watts":100}`. Success `200 OK` is the re-read device value:
`{"type":"output","level":4,"watts":100}`. Errors:
`400 invalid_request` (including unsupported watt value or runtime mutation),
`409 capability_unsupported`, `503 device_disconnected`,
`502 ble_operation_failed`. BLE I/O: serialized SET then GET; no interleaving.

### `DELETE /api/v1/device/usbc/limit/{type}`

Role: client. `{type}` is `global`, `input`, or `output`; request: none. Success
`200 OK` is the re-read device value, for example
`{"type":"output","level":0,"watts":30}`. Clearing sends the BLE delete
operation `0x02`, never opcode `0x06`. Errors: `400 invalid_request` (including
runtime), `409 capability_unsupported`, `503 device_disconnected`,
`502 ble_operation_failed`. BLE I/O: serialized DELETE then GET.

### `POST /api/v1/device/dc/bypass`

Role: client. Request `{"on":true}`. Success `200 OK`:

```json
{"enabled":true,"command":{"id":"cmd_deadbeef","operation":"dc_bypass","requested":{"on":true},"phase":"confirmed","started_at":"2026-07-17T20:00:00Z","updated_at":"2026-07-17T20:00:02Z","error":null}}
```

Errors: `400 invalid_request`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`, `504 command_timeout`.
BLE I/O: one command transaction, then up to ten seconds of telemetry
reconciliation.

### `GET /api/v1/device/dc/bypass/threshold`

Role: admin. Request: none. Success `200 OK`: `{"volts":19.6}`. Errors:
`403 admin_required`, `403 advanced_disabled`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: one GET command.

### `PUT /api/v1/device/dc/bypass/threshold`

Role: admin. Request `{"volts":19.6}`. Success `200 OK` is the observed result:
`{"volts":19.6}`. Errors: `400 invalid_request`, `403 admin_required`,
`403 advanced_disabled`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: SET then GET.

### `POST /api/v1/device/restart`

Role: client. Request: none. Success `200 OK`:
`{"status":"restarting","reconnect":"armed"}`. Errors:
`400 invalid_request` for a nonempty body, `409 capability_unsupported`,
`503 device_disconnected`,
`502 ble_operation_failed` only when failure is not the expected disconnect.
BLE I/O: restart command; disconnect (including a write error caused by it) is
success and automatic reconnect is armed for approximately 15 seconds later.

### `POST /api/v1/device/shutdown`

Role: client. Request `{"confirm":true}`. Success `200 OK`:
`{"status":"shutdown","reconnect":"disarmed"}`. Errors:
`400 invalid_request` when confirmation is absent/false,
`409 capability_unsupported`, `503 device_disconnected`,
`502 ble_operation_failed` only when failure is not the expected disconnect.
BLE I/O: writes `FM` to the factory characteristic; expected disconnect is
success and automatic reconnect is disarmed.

## Timers

The canonical timer object is:

```json
{"id":3,"status":1,"type":2,"hour":6,"minute":30,"repeat":62,"action":1}
```

`id` is `0..254`; writable `status` values are `1` (enabled) and `-1`
(disabled). Device-rendered `-2` (validation-disabled) and `-3` (expired) are
read-only. `type` is `0` one-shot, `1` daily, `2` weekly, `3` monthly;
`action` is `0` off or `1` on. `repeat` is a uint32: one-shot packs little-endian
year/month/day bytes (`2026-07-18` = `0x120707EA` = `302450666`), daily is `0`,
weekly uses bits 1=Monday through 7=Sunday, and monthly uses bits 1=day 1 through
31=day 31. Calendar dates, masks, hour `0..23`, and minute `0..59` are validated.

### `GET /api/v1/device/timers`

Role: client. Request: none. Success `200 OK`:
`[{"id":3,"status":1,"type":2,"hour":6,"minute":30,"repeat":62,"action":1}]`
(empty is `[]`). Errors: `400 invalid_request` for a nonempty body,
`409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: list then GET
each timer under one serialized ownership.

### `POST /api/v1/device/timers`

Role: client. Request has no `id`, for example
`{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`. Success
`201 Created` is the assigned, re-listed device timer:
`{"id":3,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`.
Errors: `400 invalid_request`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: ADD with BLE ID
`0xFF`, adopt reply ID, then authoritative re-list.

### `GET /api/v1/device/timers/{id}`

Role: client. Request: none. Success `200 OK` is one canonical timer object.
Errors: `400 invalid_request`, `404 not_found`,
`409 capability_unsupported`, `503 device_disconnected`,
`502 ble_operation_failed`. BLE I/O: one GET command.

### `PUT /api/v1/device/timers/{id}`

Role: client. Request omits `id`, for example
`{"status":-1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`. Success
`200 OK` is the authoritative re-listed timer with the URL ID. Errors:
`400 invalid_request`, `404 not_found`, `409 capability_unsupported`,
`503 device_disconnected`, `502 ble_operation_failed`. BLE I/O: EDIT then
authoritative re-list under one serialized ownership.

### `DELETE /api/v1/device/timers/{id}`

Role: client. Request: none. Success `200 OK`:
`{"deleted":3,"timers":[]}` where `timers` is the authoritative remaining list.
Errors: `400 invalid_request`, `404 not_found`,
`409 capability_unsupported`, `503 device_disconnected`,
`502 ble_operation_failed`. BLE I/O: DELETE then authoritative re-list.

## OTA, clock, and advanced

Every endpoint in this section is admin-only and advanced-gated. Hardware/mode
support is tested before administrative enablement: unavailable returns
`409 capability_unsupported`; supported but disabled returns
`403 advanced_disabled`. Other shared errors are `403 admin_required`,
`503 device_disconnected`, and `502 ble_operation_failed` when BLE I/O occurs.
Malformed JSON, an invalid value, or a nonempty body on a bodyless route returns
`400 invalid_request`.

### `GET /api/v1/device/ota`

Request: none. Success `200 OK`:
`{"mode":"app","cid":773,"bootloader_firmware":"1.0.3"}`. BLE I/O: OTA INFO
write/read. This API exposes INFO only; there is no erase, program, verify,
download, flash, or firmware upload route.

### `POST /api/v1/device/ota/enter`

Request `{"confirm":true}`. Success `200 OK`:
`{"mode":"ota","reconnect":"bootloader"}`. Additional error:
`400 invalid_request` when confirmation is absent/false. BLE I/O: writes `PK`;
the expected disconnect is success and reconnect targets bootloader mode.

### `POST /api/v1/device/ota/exit`

Request: none. Success `200 OK`: `{"mode":"app","reconnect":"armed"}`.
BLE I/O: OTA exit in bootloader mode; expected disconnect is success.

### `GET /api/v1/device/clock`

Request: none. When readable, success `200 OK`:
`{"available":true,"device_time":"2026-07-17T20:00:00Z","system_time":"2026-07-17T20:00:02Z","drift_seconds":-2}`.
When the Current Time characteristic is absent/unreadable by inventory, success
is `{"available":false,"device_time":null,"system_time":"2026-07-17T20:00:02Z","drift_seconds":null}`
and performs zero BLE transport I/O. BLE I/O: one characteristic read only when
inventory says it is readable.

### `POST /api/v1/device/clock/sync`

Request: none. Success `200 OK`:
`{"synced":true,"system_time":"2026-07-17T20:00:02Z"}`. BLE I/O: writes the
exact ten-byte Current Time structure with adjustment reason `0` (manual sync).

### `PUT /api/v1/device/advanced/running-mode`

Request `{"mode":1}` where `mode` is the supported unsigned device enum. Success
`200 OK`: `{"mode":1}`. Additional error: `400 invalid_request`. BLE I/O: one
running-mode SET command. This endpoint is intentionally PUT-only.

### `GET /api/v1/device/advanced/barrier-free`

Request: none. Success `200 OK`: `{"enabled":true}`. BLE I/O: one GET command.

### `PUT /api/v1/device/advanced/barrier-free`

Request `{"enabled":true}`. Success `200 OK`: `{"enabled":true}`. Additional
error: `400 invalid_request`. BLE I/O: SET then GET observed state.

### `GET /api/v1/device/advanced/usb-fw-version`

Request: none. Success `200 OK`:
`{"raw":"010409","major":1,"minor":4,"patch":9}`. BLE I/O: one GET command.

### `PUT /api/v1/device/advanced/ble-pin`

Request `{"pin":"020555"}`; the PIN is exactly six decimal digits and remains a
string so leading zeroes survive. Success `200 OK`: `{"updated":true}`; the PIN
itself is never echoed. Additional error: `400 invalid_request`. BLE I/O: one
SET-only command containing the unsigned 32-bit little-endian numeric PIN. The
new value is persisted as `ble_pin` only after success.

## Rules and legacy actions

Rule objects have this exact JSON shape (condition-specific fields may be
omitted):

```json
{"name":"low_battery","enabled":true,"condition":"battery_level","op":"below","percent":15,"hold":600000000000,"hysteresis_margin":5,"actions":["dc_off"],"confirm_shutdown":false}
```

Conditions are `input_power` (`state`: `present|absent`), `battery_level`
(`op`: `below|above`, `percent`), `port_power` (`port`: `dc|usbc`, `op`,
`watts`), and `schedule` (`cron`, five fields). Durations are Go JSON duration
nanoseconds. `repeat_every` is optional and omitted when zero. Actions are
`dc_on`, `dc_off`, `usbc_on`, `usbc_off`, `bypass_on`,
`bypass_off`, `restart`, `shutdown`, and `webhook:URL`. Shutdown requires
`confirm_shutdown:true`.

| Endpoint | Role | Request | Success | Endpoint-specific errors | BLE I/O |
|---|---|---|---|---|---|
| `GET /api/v1/rules` | client | none | `200` array, empty `[]` | none beyond global body/auth errors | none |
| `POST /api/v1/rules` | admin | complete rule object | `200` stored rule; zero hysteresis defaults to `5` | `400 invalid_request`, `500 internal_error` | none |
| `PUT /api/v1/rules/{name}` | admin | complete rule object; URL name wins | `200` stored rule | `400 invalid_request`, `500 internal_error` | none |
| `DELETE /api/v1/rules/{name}` | admin | none | `200 {"deleted":"low_battery"}` | `404 not_found`, `500 internal_error` | none |
| `POST /api/v1/device/action` | client; admin for `webhook:URL` | `{"action":"dc_off"}` | `200 {"ok":"dc_off"}` | `400 invalid_request`, `403 admin_required` for a managed-client webhook, `502 ble_operation_failed` | according to action; webhook may perform HTTP |

The action endpoint is deprecated in favor of granular routes. Its successful
body remains legacy-compatible; its errors use the canonical envelope. Rule
mutation and any manual `webhook:URL` action requires admin because those
operations persist automation or originate an HTTP request from the router.
Managed clients may still read rules and use every ordinary Link-Power action.

### Power-loss shutdown preset

The two administration panels expose the reserved `no_input_shutdown` preset
as a dedicated **Power-loss shutdown** card. It is available under
**Applications → Wattline** in the native GL panel and **Services → Wattline**
in LuCI. The canonical rule watches `input_power` with `state:"absent"`, has a
10-minute default `hold` of `600000000000`, contains the `shutdown` action, and
requires `confirm_shutdown:true`.

Input must remain absent continuously for the entire hold. Input returning
cancels the current countdown and re-arms the rule. A BLE disconnect resets the
hold; time without telemetry is never counted. At expiry the engine attempts
the shutdown trigger once. A displayed “last fired” records an attempted
trigger, not proof that shutdown succeeded; action failures remain available in
daemon logs and events.

For a compatible reserved rule—one that still watches absent input and contains
a shutdown action—the GUI owns only `enabled`, `hold`, and `confirm_shutdown`.
It preserves every other valid field and action, including additional webhooks.
An incompatible reserved rule is not overwritten unless the operator chooses
an explicit, confirmed **Reset preset**, which replaces it with the canonical
rule using the current card settings. Other named rules are never changed.

Link-Power supplies this router, so a successful shutdown also removes power
from the GL-X3000. Recovery is hardware-driven: after input returns, Link-Power
must wake, the GL-X3000 boots, and `wattlined` reconnects. The daemon does not
and cannot promise a software wake while its router is off.

## BLE-device pairing

These authenticated routes pair the router to a Link-Power over BlueZ. They use
`ble_pin` (default `020555`) and are unrelated to API-client `pairing_pin`.

| Endpoint | Role | Request | Success | Endpoint-specific errors | BLE I/O |
|---|---|---|---|---|---|
| `GET /api/v1/pairing/status` | client | none | `200 {"stage":"idle","devices":[]}`; optional `error`, `target`; device entries are `{"mac":"DC:04:5A:EB:72:2B","name":"Link-Power-2","rssi":-60,"paired":false}` | `400 invalid_request` for a nonempty body; `409 capability_unsupported` when BlueZ pairing is unavailable | none |
| `POST /api/v1/pairing/scan` | client | none | `202 {"status":"scanning"}` | `400 invalid_request` for a nonempty body; `409 operation_in_progress`, `409 capability_unsupported`, `502 ble_operation_failed` | asynchronous BlueZ scan |
| `POST /api/v1/pairing/pair` | client | `{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}` (`pin` may be empty to retain configured PIN) | `202 {"status":"pairing"}` | `400 invalid_request`, `409 operation_in_progress`, `409 capability_unsupported` | asynchronous BlueZ pair/trust and BLE reconnect proof; asynchronous failure appears in status |
| `DELETE /api/v1/pairing/device/{mac}` | client | none | `200 {"status":"removed"}` | `400 invalid_request` for an invalid MAC or nonempty body, `409 operation_in_progress` while scan/pair is active, `409 capability_unsupported`, `502 ble_operation_failed` | BlueZ unpair, not a device command |

## API-client pairing

`pairing_pin` is a random, zero-padded six-digit enrollment PIN. Opening mode
defaults to a five-minute TTL. `pairing_always_on=1` keeps enrollment available
but rotates the PIN at the configured TTL. A valid PIN may enroll multiple
clients while its window is open; every exchange issues a distinct token.
Failures are limited to five per source IP and twenty globally per minute;
proxy headers do not override the TCP source. Wrong, expired, closed,
rate-limited, and source-capacity failures are indistinguishable. No pairing
status or log exposes a PIN to non-admins.

### `POST /api/v1/pair`

Role: public. Request `{"pin":"123456","label":"Keith's iPhone"}`. `label`
is nonempty valid UTF-8, at most 128 bytes, has no leading/trailing whitespace,
and contains no control characters. Success
`201 Created`; this is the one and only return of the 256-bit secret:

```json
{"token":"wlt_7dd64d22b0c14e7bb86af967b63835f9f971b4234e83277b646d58e184a44af5","token_metadata":{"id":"7dd64d22b0c14e7b","label":"Keith's iPhone","created_at":"2026-07-17T20:00:00Z","last_seen_at":null,"bootstrap":false},"device_id":"DC:04:5A:EB:72:2B","base_urls":{"https":"https://wattline.lan:8378/api/v1","http":"http://wattline.lan:8377/api/v1"},"tls_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","magic_dns_name":"wattline.example.ts.net"}
```

`base_urls` contains only enabled listeners. `tls_sha256` is the served
certificate's lowercase DER SHA-256, or an empty string when HTTPS is disabled;
`magic_dns_name` is empty when Tailscale MagicDNS cannot be resolved.

Errors: `400 invalid_request`, `401 invalid_or_expired_pin`, and
`500 internal_error` when enrollment persistence or connection metadata is
unavailable. BLE I/O: none.

### `GET /api/v1/pairing-mode`

Role: admin. Request: none. Success `200 OK`:
`{"open":true,"expires_at":"2026-07-17T20:05:00Z","pin":"123456"}`. When
closed: `{"open":false,"expires_at":"0001-01-01T00:00:00Z"}` (PIN omitted).
Errors: `400 invalid_request` for a nonempty body, authentication/role errors,
or `500 internal_error` if pairing administration is unavailable. BLE I/O: none.

### `POST /api/v1/pairing-mode`

Role: admin. Request: none. Success `200 OK`:
`{"open":true,"expires_at":"2026-07-17T20:05:00Z","pin":"123456"}`. Errors:
`400 invalid_request` for a nonempty body, authentication/role errors, or
`500 internal_error` if pairing administration is unavailable. BLE I/O: none.

### `DELETE /api/v1/pairing-mode`

Role: admin. Request: none. Success `200 OK`: `{"open":false}`. Errors:
`400 invalid_request` for a nonempty body, authentication/role errors, or
`500 internal_error` if pairing administration is unavailable. BLE I/O: none.

### `GET /api/v1/pairing-mode/qr.png`

Role: admin. Request: none and no query PIN is accepted. Success `200 OK` is a
PNG encoding the current QR payload, with `Content-Type: image/png` and
`Cache-Control: no-store`. Errors: `400 invalid_request` for any query PIN,
`409 capability_unsupported` when pairing mode is closed, plus auth/role errors.
An unavailable pairing manager or connection metadata returns
`500 internal_error`. BLE I/O: none.

## Tokens

### `GET /api/v1/tokens`

Role: admin. Request: none. Success `200 OK` returns metadata only:

```json
[{"id":"bootstrap","label":"Bootstrap administrator","created_at":"2026-07-17T19:00:00Z","last_seen_at":"2026-07-17T20:00:00Z","bootstrap":true},{"id":"7dd64d22b0c14e7b","label":"Keith's iPhone","created_at":"2026-07-17T20:00:00Z","last_seen_at":null,"bootstrap":false}]
```

No token secret or hash is returned. Errors: `400 invalid_request` for a
nonempty body, plus auth/role errors. BLE I/O: none.

### `DELETE /api/v1/tokens/{id}`

Role: admin. Request: none. Success `200 OK`:
`{"revoked":"7dd64d22b0c14e7b"}`; revocation is immediate. Errors:
`400 invalid_request` for ID `bootstrap`, an empty ID, or a nonempty body;
`404 not_found`; `500 internal_error` on persistence failure; plus auth/role
errors. BLE I/O: none.

Immediate revocation rejects subsequent requests and closes every open SSE
stream authenticated by that managed token. It does not close streams belonging
to other managed tokens or the bootstrap administrator.

Managed secrets are random 256-bit values. The mode-`0600` token store defaults
to `/etc/wattline/tokens.json` and contains IDs, labels, timestamps, and lowercase
SHA-256 secret hashes only. Authentication updates last-seen in memory;
persistence is coalesced to at most once per hour to limit flash wear.

## Settings and TLS

### `GET /api/v1/settings`

Role: admin. Request: none. Success `200 OK`:

```json
{"http":{"enabled":true,"addr4":"0.0.0.0","addr6":"::","port":8377},"https":{"enabled":true,"addr4":"0.0.0.0","addr6":"::","port":8378},"tls":{"cert":"/etc/wattline/tls/server.crt","key":"/etc/wattline/tls/server.key","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},"token_store":"/etc/wattline/tokens.json","pairing_ttl":"5m0s","pairing_always_on":false,"advanced":false,"mdns":{"enabled":true,"interfaces":["br-lan"]},"wan_access":false,"ble_pin":"020555"}
```

Bearer secrets and private-key bytes are never included. Errors:
`400 invalid_request` for a nonempty body, `500 internal_error` when settings are
unavailable, plus auth/role errors. BLE I/O: none.

### `PUT /api/v1/settings`

Role: admin. Despite the approved `PUT` method, this endpoint has merge (PATCH-like)
semantics: omitted fields and omitted nested-object members are preserved;
supplied scalar, array, or nested members replace their stored value; an explicit
empty array clears that array; and every unknown field or read-only
`tls.sha256` is rejected. For example, `{"advanced":true,"wan_access":false}`
changes exactly those two fields and preserves every listener, TLS, pairing, and
mDNS field. Success `200 OK` returns the complete merged settings object from GET
plus `"restart_required":true` when a supplied listener, TLS, mDNS, or firewall
field changed, otherwise `false`. Errors: `400 invalid_request` for malformed
JSON, an unknown/read-only field, invalid value, or invalid resulting listener
configuration, plus auth/role errors. BLE I/O: none.

For a `token_store` change, a successful durable `SaveMain` activates the target
store. At that commit boundary, all old-store managed SSE streams close and
old-store-only managed tokens fail future requests because authentication now
uses the target store; the bootstrap SSE remains open. Conversely, all managed-token
records present in the target store become active. Secrets represented by
identical or copied secret hashes can reconnect even though their old-store SSE
streams closed. The old store file and its managed tokens are not revoked or
deleted, and switching back to that path can reactivate them. A persistence
failure preserves the old authenticator and its streams: the runtime store swap
is rolled back and no cutover termination is published.

### `POST /api/v1/tls/rotate`

Role: admin. Request `{"confirm":true}`. Success `200 OK`:
`{"sha256":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","restart_required":true}`.
Errors: `400 invalid_request` when confirmation is absent/false, plus auth/role
errors; certificate-generation failures return `500` with the canonical error
envelope and code `internal_error`. BLE I/O: none.

First-boot initialization generates an ECDSA P-256 self-signed certificate at
`/etc/wattline/tls/server.crt` and a mode-`0600` key at
`/etc/wattline/tls/server.key` without an OpenSSL runtime dependency. Fingerprints
are SHA-256 of DER certificate bytes, rendered as 64 lowercase hexadecimal digits.

### Certificate pinning and trust

In the daemon's default pinned self-signed mode, trust requires both the exact
leaf-certificate pin and the correlated Wattline device ID. For the advertised
endpoint, that explicit policy replaces both public-CA chain validation and
hostname/SAN validation; merely accepting any self-signed certificate or
disabling platform trust checks is not pinning. The pin is the SHA-256 digest
of the exact leaf certificate DER bytes, not of PEM text, the public key, or a
textual fingerprint.

The first-boot certificate contains the daemon's startup hostname plus
`localhost`, `127.0.0.1`, and `::1`. A later MagicDNS name, preferred LAN name,
or address may therefore be absent from its SANs. In pinned self-signed mode
the client verifies the exact DER pin and correlated device ID instead of
requiring those advertised names to match a SAN. If an operator replaces the
certificate with one issued by a public CA, public-CA mode uses normal chain
and hostname/SAN validation unless the client has independently enabled an
explicit pin policy; a configured pin mismatch remains a hard failure.

A client obtains the candidate pin from the QR `tls` parameter, mDNS `tls` TXT
value, or `tls_sha256` in the public pairing response received over LAN HTTP or
an encrypted VPN path. It MUST establish that value's authenticity through the
admin-displayed QR, an out-of-band comparison, or an already trusted VPN. mDNS
and unauthenticated LAN HTTP alone are discovery/TOFU sources, not protection
against an active LAN attacker. The client MUST correlate the accompanying
device `id`, then connect to HTTPS, hash the presented leaf DER certificate, and
compare all 32 bytes in constant time. Clients MUST verify the certificate
before sending any bearer token. A missing or mismatched pin is a hard connection
failure: a client MUST NOT silently downgrade to HTTP,
automatically accept a replacement certificate, or disclose its token. Plain
HTTP is a separate explicit transport choice for trusted LAN enrollment or an
already encrypted VPN; its availability is never permission to fall back.

`POST /api/v1/tls/rotate` must be called through the currently authenticated,
pinned administrator channel. Its `sha256` is the new certificate pin and
`restart_required:true` means the active HTTPS listener continues serving the
old certificate until the daemon restarts. An administrator can therefore save
the returned new pin before restarting. After restart the client must require
the new pin and the old pin MUST be rejected. A client that did not receive this
authenticated rotation handoff must re-enroll or confirm the new pin through an
out-of-band trusted source; it must not infer trust merely because the device ID
is unchanged. Rotation does not revoke bearer tokens. After restart, QR, pairing
responses, settings, and mDNS publish the newly served pin.

## mDNS

The daemon publishes `_wattline._tcp` only on configured LAN interfaces
(default `br-lan`) and only after a stable device ID is known from UCI or a BLE
handshake. The advertised port is HTTPS when enabled, otherwise HTTP. The in-process
responder updates identity/TLS data atomically and never publishes on WAN.

TXT keys are exactly `ver`, `api`, `id`, `model`, `cid`, `features`, `tls`, and `auth`;
no other key is part of v1:

```text
ver=0.1.0
api=1
id=DC:04:5A:EB:72:2B
model=BP4SL3V2
cid=0305
features=00000fff
tls=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
auth=pin
```

`cid` is exactly four lowercase hex digits without `0x`, or empty while unknown.
`features` is exactly eight lowercase hex digits, or empty while unknown. `model`
may be empty while unknown. `tls` is the lowercase certificate fingerprint, or
`none` when HTTPS is disabled. `auth` is always `pin`. mDNS performs no BLE I/O.

## QR payload

The QR PNG contains one UTF-8 URI in this parameter order:

```text
wattline://pair?v=1&id=DEVICE_ID&host=PREFERRED_HOST&http=8377&https=8378&pin=123456&tls=CERT_SHA256
```

Values use RFC 3986 query percent-encoding: UTF-8 bytes outside unreserved
`A-Z a-z 0-9 - . _ ~` are uppercase `%HH`; spaces are `%20`, never `+`; the MAC
colons in `id` are `%3A`. Parameter names and decimal ports are not escaped.
Thus a concrete payload begins:

```text
wattline://pair?v=1&id=DC%3A04%3A5A%3AEB%3A72%3A2B&host=wattline.lan&http=8377&https=8378&pin=123456&tls=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

`v`, `id`, `host`, and `pin` are required. `host` is MagicDNS when available,
otherwise the preferred LAN hostname/address. Omit `http` when HTTP is disabled;
omit `https` and `tls` when HTTPS is disabled. Never emit an empty optional
parameter, a bootstrap/managed token, a private key, or `ble_pin`. A missing
optional parameter is distinct from a value of zero or `none`.

## Compatibility routes

This inventory is exhaustive for the routes registered by the daemon. Successful
bodies remain wire-compatible; all errors use the exact `E(code)` bodies in the
error-envelope section. Every row inherits the global request-body and
authentication errors: a nonempty `GET`/`DELETE` body returns `400
E(invalid_request)`, and a missing or invalid bearer token returns `401
E(unauthorized)`. Admin rows also inherit `403 E(admin_required)` for a valid
client token. Those inherited errors are omitted from “Additional errors.” “No
body” means an exactly zero-byte request body.

### Cached state compatibility routes

| Endpoint | Role | Exact request | Exact success | Additional errors (status, body, condition) | BLE I/O |
|---|---|---|---|---|---|
| `GET /api/v1/status` | client | no body | `200 {"connected":true,"device":{"model":"BP4SL3V2","hw_rev":"V2","firmware":"1.4.9","bootloader_firmware":"1.0.3","mac":"DC:04:5A:EB:72:2B","cid":773,"features":4095,"mode":"app"},"rules":[]}` | none | none |
| `GET /api/v1/telemetry` | client | no body | `200` with exactly the one complete cached Snapshot JSON defined under the primary `GET /api/v1/telemetry` section, including its top-level `identity` and `commands`; this compatibility contract is not a reduced subset | none | none |
| `GET /api/v1/history` | client | no body | `200 [{"at":"2026-07-17T19:59:00Z","level":77,"status":1,"dc_w":12.0,"typec_w":20.0}]` (empty is exactly `[]`) | none | none |
| `GET /api/v1/events` | client | no body | `200`, `Content-Type: text/event-stream`, then the exact complete-snapshot framing specified above | before streaming begins, `500 E(internal_error)` if response streaming is unavailable; managed-token revocation, a successful token-store cutover, or slow-subscriber overflow terminates the stream; after `200` begins, termination has no JSON error body | none |

### Rule and action compatibility routes

The exact rule used in the rows below is
`{"name":"low_battery","enabled":true,"condition":"battery_level","op":"below","percent":15,"hold":600000000000,"hysteresis_margin":5,"actions":["dc_off"],"confirm_shutdown":false}`.

| Endpoint | Role | Exact request | Exact success | Additional errors (status, body, condition) | BLE I/O |
|---|---|---|---|---|---|
| `GET /api/v1/rules` | client | no body | `200 [{"name":"low_battery","enabled":true,"condition":"battery_level","op":"below","percent":15,"hold":600000000000,"hysteresis_margin":5,"actions":["dc_off"],"confirm_shutdown":false}]` | none | none |
| `POST /api/v1/rules` | admin | exact rule above | `200` and the exact same rule JSON | `400 E(invalid_request)` for malformed JSON, invalid condition/action, or shutdown without confirmation; `500 E(internal_error)` when persistence fails | none |
| `PUT /api/v1/rules/{name}` | admin | exact rule above; for path `/api/v1/rules/low_battery` | `200` and the exact same rule JSON (URL name replaces a different body name) | `400 E(invalid_request)` for malformed JSON or invalid rule/action; `500 E(internal_error)` when persistence fails | none |
| `DELETE /api/v1/rules/{name}` | admin | no body; for path `/api/v1/rules/low_battery` | `200 {"deleted":"low_battery"}` | `404 E(not_found)` when no rule has that name; `500 E(internal_error)` when persistence fails | none |
| `POST /api/v1/device/action` | client; manual `webhook:URL` requires admin | `{"action":"dc_off"}` | `200 {"ok":"dc_off"}` | `400 E(invalid_request)` for malformed JSON or an unknown/empty action; `403 E(admin_required)` when a managed client submits a webhook; `502 E(ble_operation_failed)` when any device action or webhook fails | one device operation for device actions; outbound HTTP only for an admin `webhook:URL` |

### BLE-device pairing compatibility routes

| Endpoint | Role | Exact request | Exact success | Additional errors (status, body, condition) | BLE I/O |
|---|---|---|---|---|---|
| `GET /api/v1/pairing/status` | client | no body | `200 {"stage":"idle","devices":[{"mac":"DC:04:5A:EB:72:2B","name":"Link-Power-2","rssi":-60,"paired":false}]}` | `400 E(invalid_request)` for a nonempty body; `409 E(capability_unsupported)` when platform/adapter pairing support is unavailable | none |
| `POST /api/v1/pairing/scan` | client | no body | `202 {"status":"scanning"}` | `400 E(invalid_request)` for a nonempty body; `409 E(operation_in_progress)` while scan/pair is active; `409 E(capability_unsupported)` when pairing support is unavailable; `502 E(ble_operation_failed)` if the scan cannot be started | asynchronous BlueZ scan, not a device command |
| `POST /api/v1/pairing/pair` | client | `{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}` | `202 {"status":"pairing"}` | `400 E(invalid_request)` for malformed JSON, invalid MAC, or a PIN string containing non-digits or more than six digits (empty is allowed); `409 E(operation_in_progress)` while scan/pair is active; `409 E(capability_unsupported)` when pairing support is unavailable | asynchronous BlueZ pair/trust and BLE reconnect proof; later failure is reported by status |
| `DELETE /api/v1/pairing/device/{mac}` | client | no body; for path `/api/v1/pairing/device/DC:04:5A:EB:72:2B` | `200 {"status":"removed"}` | `400 E(invalid_request)` for invalid MAC or a nonempty body; `409 E(operation_in_progress)` when unpair is busy with scan/pair; `409 E(capability_unsupported)` when pairing support is unavailable; `502 E(ble_operation_failed)` when BlueZ unpair fails | BlueZ unpair, not a device command |

### Deprecated device-control aliases

| Endpoint | Role | Exact request | Exact success | Additional errors (status, body, condition) | BLE I/O / replacement |
|---|---|---|---|---|---|
| `GET /api/v1/device/usbc-limit` | client | no body | `200 {"global":{"level":4,"watts":100},"input":{"level":3,"watts":65},"output":{"level":4,"watts":100},"runtime":{"level":-1,"watts":0}}` | `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` when any GET fails | four GET commands; replace with per-type canonical GET |
| `POST /api/v1/device/usbc-limit` | client | set: `{"type":"output","watts":100}`; clear: `{"type":"output","clear":true}`; `clear:true` ignores `watts` if supplied | set: `200 {"watts":100,"level":4}` from mutation plus authoritative re-GET; clear: `200 {"status":"cleared"}` after mutation plus authoritative re-GET | `400 E(invalid_request)` for malformed JSON, type outside `global|input|output`, or—only when `clear` is false—watts outside `30|45|60|65|100|140`; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on SET/DELETE/re-GET failure | SET then GET, or DELETE then GET; replace with canonical PUT/DELETE |
| `GET /api/v1/device/bypass-threshold` | admin | no body | `200 {"volts":19.6}` | `403 E(advanced_disabled)` when policy is off; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on GET failure | one GET; replace with canonical threshold GET |
| `POST /api/v1/device/bypass-threshold` | admin | `{"volts":19.6}` | `200 {"volts":19.6}` from mutation plus authoritative re-GET | `400 E(invalid_request)` for malformed JSON or volts not in `(0,60]`; `403 E(advanced_disabled)` when policy is off; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on SET/re-GET failure | SET then GET; replace with canonical threshold PUT |
| `GET /api/v1/device/schedules` | client | no body | `200 [{"id":3,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}]` (empty is exactly `[]`) | `400 E(invalid_request)` for a nonempty body; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on list/GET failure | list then timer GETs; replace with canonical timer list |
| `POST /api/v1/device/schedules` | client | add: `{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`; edit: `{"id":3,"status":-1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}` | add/edit returns the selected timer after mutation plus authoritative re-list, for example `200 {"id":3,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}` | `400 E(invalid_request)` for malformed JSON, ID outside `0..254`, type outside `0..3`, hour outside `0..23`, minute outside `0..59`, action outside `0..1`, or invalid status/repeat structure; `404 E(not_found)` when an edit ID is absent; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on ADD/EDIT/re-list failure | ADD/EDIT then re-list; replace with canonical timer POST/PUT |
| `DELETE /api/v1/device/schedules/{id}` | client | no body; for path `/api/v1/device/schedules/3` | mutation plus authoritative re-list, then `200 {"status":"deleted"}` | `400 E(invalid_request)` for a nonempty body, nondecimal ID, or ID outside `0..254`; `404 E(not_found)` when timer is absent; `409 E(capability_unsupported)` when unsupported; `503 E(device_disconnected)` when disconnected; `502 E(ble_operation_failed)` on DELETE/re-list failure | DELETE then re-list; replace with canonical timer DELETE |

These 20 method/path entries are the complete compatibility inventory. No
compatibility route permits unauthenticated actual requests; only CORS preflight
bypasses authentication.

## On-target caveats

Unit tests cannot prove the following; they require a GL-X3000 and/or a real
Link-Power and must remain explicitly unverified until exercised:

- exact control/reply frames, DC/USB-C/bypass telemetry reconciliation, timeout
  timing, Current Time readability and drift, and timer persistence/rendered
  `-2`/`-3` states;
- restart reconnect timing, shutdown reconnect disarming, OTA app/bootloader
  entry/INFO/exit, running mode, barrier-free mode, USB firmware, and BLE-PIN;
- dual-stack HTTP/HTTPS reachability, certificate pinning, reboot persistence,
  LAN-only `_wattline._tcp` visibility and dynamic TXT values;
- Tailscale MagicDNS, optional WireGuard reachability, default WAN rejection and
  explicit warned WAN access; and
- LuCI/GL BLE pairing, API-client pairing/QR, token revocation, settings, controls,
  rules, webhooks, SSE, and persistence across reboot.

OTA firmware download, erase, program, verify, and flashing are outside this API.
Rejected BLE opcodes `0x05`, `0x12`, and `0xF0` have no HTTP routes.
