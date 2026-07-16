# PeakDo Link-Power — Full API / Protocol Reference

Reverse-engineered from the PWA at `https://pwa.peakdo.ca/link-power-1/` (app version **1.1.1**).
Source files were fetched and read directly (no minification); this document reflects the exact byte
layouts, UUIDs, and endpoints used by the official web app.

**Live-verified 2026-07-14** against real hardware: `Link-Power-2`, model `BP4SL3V2`, hw rev `V5#0305`
(LP2_V5, CID `0x0305`), app firmware **1.4.9**, OTA bootloader **2.0.2**. Items marked ✅ were confirmed
byte-for-byte on this device; deviations from the original PWA-derived documentation are called out inline.

> **TL;DR — there is no cloud/REST control API.** The Link-Power power bank is controlled entirely over
> **Bluetooth Low Energy (BLE GATT)**. The PWA uses the browser Web Bluetooth API. The *only* HTTP API is a
> read-only firmware CDN (`api.peakdo.ca/fw-api`) used for over-the-air update checks. To build a Swift app
> you use **CoreBluetooth**; to build a CLI/library you use any BLE stack (e.g. `bleak` in Python,
> `noble` in Node, `btleplug` in Rust, CoreBluetooth on macOS).

---

## 1. Device discovery

- **Advertised name:** starts with `Link-Power` in normal (app) mode, or `PeakDo-OTA` while in firmware-update mode.
- **Primary service advertised:** the Link-Power service (16-bit UUID `0x5301`).
- The PWA scan filters are:
  - `{ services: [0x5301], namePrefix: "Link-Power" }`
  - `{ services: [0x5301], namePrefix: "PeakDo-OTA" }` (OTA mode)
- Optional/standard services also used once connected: `device_information` (`0x180A`), `current_time` (`0x1805`), and historically `battery_service` (`0x180F`).

All 16-bit UUIDs expand to the Bluetooth base UUID, e.g. `0x5301` → `00005301-0000-1000-8000-00805f9b34fb`.

---

## 2. GATT map

### 2.1 Link-Power service — `0x5301`

| Characteristic | UUID (16-bit) | Full UUID | Ops | Purpose |
|---|---|---|---|---|
| OTA | `0x4301` | `00004301-…` | write, read | Firmware update + mode/info query |
| **Command** | `0x4302` | `00004302-…` | write, read | Main control channel (write command → read response) |
| ExtBatteryInfo | `0x4303` | `00004303-…` | read, notify | Battery telemetry |
| DcPortStatus | `0x4304` | `00004304-…` | read, notify | DC output port telemetry |
| TypeCPortStatus | `0x4305` | `00004305-…` | read, notify | USB-C port telemetry |
| FactoryMode | `0x4310` | `00004310-…` | write | Shutdown / factory magic writes (✅ shutdown verified) |

### 2.2 Standard Device Information service — `0x180A`

| Characteristic | UUID | PWA field | Notes |
|---|---|---|---|
| Model Number | `0x2A24` | `model` | e.g. `BP4SL3V1`, `BP4SL3V2`, `PK-LINK-POWER-1` |
| Hardware Revision | `0x2A27` | `variant` | e.g. `V6#0104#r3` (encodes variant / CID / revision) |
| Firmware Revision | `0x2A26` | `otaVersion` | OTA-bootloader version string |
| Software Revision | `0x2A28` | `fwVersion` / `appVersion` | Application firmware version `a.b.c[_suffix]` |

### 2.3 Standard Current Time service — `0x1805`

| Characteristic | UUID | Ops | Purpose |
|---|---|---|---|
| Current Time | `0x2A2B` | write | Set device RTC (see §5) |

---

## 3. Command channel protocol (characteristic `0x4302`)

**Transaction model:** write the command bytes with response, then **read** the same characteristic to get
the reply. (`writeValueWithResponse` then `readValue`.) The PWA helper is:

```
write(0x5301, 0x4302, cmd)      // Uint8Array
response = read(0x5301, 0x4302) // DataView, only if a reply is expected
```

### 3.1 Frame layout

Request bytes:
```
[ CMD, ACTION, ...payload ]
```
Reply bytes (typical):
```
[ CMD_echo, ACTION|0x80, RESULT, ...payload ]
   byte0      byte1       byte2
```
`RESULT` (byte 2) is `0x00` on success. `0xFF` is used by *get power limit* to mean "unset". Other non-zero
values are error codes. (Not every command returns the full 3-byte prefix — see each command.)

> ✅ **Live-confirmed** across `0x01`, `0x02`, `0x06`, `0x13`, `0x14`, `0xFE`: byte 0 always echoes the
> command, byte 1 is always `ACTION | 0x80`. Exception on the RESULT byte: `DC_BYPASS_CONTROL` (`0x14`)
> returned **non-zero** result codes (`0xFF`, `0xFD`) even when the toggle took effect — see §3.4.

### 3.2 Actions (`ACTION` byte)

| Name | Value |
|---|---|
| GET | `0x00` |
| SET | `0x01` |
| DEL | `0x02` |

### 3.3 Command opcodes (`CMD` byte)

| Command | Opcode | Used by PWA | Live status | Notes |
|---|---|---|---|---|
| `DC_CONTROL` | `0x01` | ✅ | ✅ works | Enable/disable DC output port |
| `TYPEC_POWER_LIMIT` | `0x02` | ✅ | ✅ works | Get/set USB-C power limit level |
| `BARRIER_FREE_MODE` | `0x03` | ❌ | ✅ works | u8 flag, get/set (see §3.4) |
| `BLE_PIN` | `0x04` | ✅ | ✅ SET only | GET/DEL return error `0xFC` |
| `IP2366_REG_DEFAULT_VALUE` | `0x05` | ❌ | ❌ `0xFD` | Rejected, even in factory mode |
| `SCHEDULED_ON_OFF` | `0x06` | ✅ | ✅ works | Scheduled on/off timers (CRUD) |
| `DEVICE_ID` | `0x10` | ❌ | ✅ works | Returns the 6-byte BT MAC (see §3.4) |
| `RESTART` | `0x11` | ✅ | ✅ works | Reboot device (no reply — link drops instantly) |
| `IP2366_REG_VALUE` | `0x12` | ❌ | ❌ `0xFD` | Rejected, even in factory mode |
| `TYPEC_CONTROL` | `0x13` | ✅ | ✅ works | USB-C input/output enable control |
| `DC_BYPASS_CONTROL` | `0x14` | ✅ | ✅ works* | Toggles, but result byte is non-zero (see §3.4) |
| `DC_BYPASS_THRESHOLD` | `0x15` | ❌ | ✅ works | Get/set, SFLOAT volts (see §3.4) |
| `GET_USB_FW_VERSION` | `0x17` | ❌ | ✅ works | USB-IC firmware version (see §3.4) |
| `RUNNING_MODE_CONTROL` | `0xE0` | ✅ | ✅ works | **Does reply** (`e0 81 00`), contra the PWA's fire-and-forget |
| `TEST` | `0xF0` | ❌ | ❌ `0xFD` | Rejected, even in factory mode |
| `FEATURES` | `0xFE` | ✅ | ✅ works | Query device feature bitmask |

Result codes observed live: `0x00` success · `0xFF` "unset" (get-limit) or bypass-off ack ·
`0xFD` command unavailable/rejected (IP2366/TEST; also bypass-on ack) · `0xFC` action unsupported
(BLE_PIN GET/DEL).

> The three `0xFD` opcodes (`0x05`, `0x12`, `0xF0`) answer with an error frame on fw 1.4.9 even after
> switching to factory running mode, so their payload shapes remain **unverified**. Everything else in
> this table — including `RESTART`, `RUNNING_MODE_CONTROL`, both §3.5 magic writes, and the OTA
> bootloader handshake (§9.1–9.3, END) — was exercised against live hardware on 2026-07-14.

### 3.4 Verified commands (exact bytes)

#### DC output on/off — `DC_CONTROL` ✅
```
Request:  [0x01, 0x01, op]      op: 0 = off, 1 = on
Reply:    [0x01, 0x81, 0x00]    RESULT at byte 2 (0 = OK)
```
✅ Live: reply `01 81 00` both directions; `DcPortStatus.enabled` followed within ~1 s.

#### USB-C power limit — `TYPEC_POWER_LIMIT` ✅
Power **levels**: `0=30W, 1=45W, 2=60W, 3=65W, 4=100W, 5=140W` (`-1` = not set).
Limit **types**: `1=global, 2=input, 3=output, 4=runtime` (runtime is read-only).
```
Get:      [0x02, 0x00, type]
  Reply:  byte2 RESULT (0 = ok, 0xFF = unset), byte3 = level (only if RESULT==0)

Set:      [0x02, 0x01, type, level]     type 1..3, level -1..5
  Reply:  byte2 RESULT (0 = ok)

Clear:    [0x02, 0x02, type]            TYPEC_POWER_LIMIT + DEL
  Reply:  byte2 RESULT (0 = ok)
```
✅ Live (fw 1.4.9, LP2): get/set work exactly as above (`02 80 00 <level>` / `02 81 00`).
Two corrections to the original doc:

- **`0xFF` "unset" was only ever observed for type 4 (runtime)** — with no PD sink attached the reply is
  `02 80 ff`. Types 1–3 always returned a level, even after DEL.
- **DEL does not produce an "unset" state — it resets the limit to the device default** (level 3 = 65 W on
  this unit). Clearing all three types and re-reading still returned level 3 for each.

> **Bug in PWA — confirmed live:** `unsetPowerLimit()` sends `[0x06, 0x02, type]` (`SCHEDULED_ON_OFF`
> opcode instead of `TYPEC_POWER_LIMIT`). The device **accepts this frame and replies success**
> (`06 82 00`) but it is a silent no-op: the power limit is unchanged and the timer list is untouched.
> The working clear command is `[0x02, 0x02, type]` as shown above.

#### USB-C output enable — `TYPEC_CONTROL` ✅
Byte 2 is a state selector; `0x02` = output-enable bit. Byte 3 is the value.
```
Request:  [0x13, 0x01, 0x02, op]   op: 0 = output off, 1 = output on
Reply:    [0x13, 0x81, 0x00]
```
Port `mode` field (reported in TypeCPortStatus, §4.3): `0=disabled, 1=input only, 2=output only, 3=all enabled`.

✅ Live: output-off moved `mode` 3 → 1 (input only), output-on restored 3. Note `TypeCPortStatus.enabled`
(byte 0) stayed `1` throughout — the `mode` field, not `enabled`, reflects this toggle. While output was
disabled the `isDCInput` byte read `1` (port idle, nothing attached).

#### DC bypass toggle — `DC_BYPASS_CONTROL` ✅ (with caveats)
```
Request:  [0x14, 0x01, op]     op: 0 = off, 1 = on
Reply:    byte2 RESULT — non-zero even on success (see below)
```
✅ Live: the toggle **works**, but the RESULT byte does not follow the 0-on-success convention:
`[0x14,0x01,0x00]` returned `14 81 ff` yet bypass turned off; `[0x14,0x01,0x01]` returned `14 81 fd` and
bypass did **not** re-engage immediately — it came back on by itself some seconds later (bypass engagement
appears asynchronous/conditional on the power path). The PWA ignores this reply entirely and blindly flips
its UI state (`views_main-view.js` ~line 726, readback commented out) — do the same, or poll
`DcPortStatus` byte 8 for the real state. Meaning of `0xFF`/`0xFD` codes unknown.

#### Change BLE PIN — `BLE_PIN` ✅ (SET only)
PIN is an unsigned integer 0–999999, little-endian 32-bit.
```
Set:      [0x04, 0x01, pin & 0xFF, (pin>>8)&0xFF, (pin>>16)&0xFF, (pin>>24)&0xFF]
  Reply:  [0x04, 0x81, 0x00]
Get:      [0x04, 0x00]   → [0x04, 0x80, 0xFC]   (unsupported — cannot read the PIN back)
Del:      [0x04, 0x02]   → [0x04, 0x82, 0xFC]   (unsupported)
```
✅ Live: SET `000000` succeeded; GET/DEL always answer `0xFC`, so a set PIN is write-only and unverifiable
over the protocol.

#### Barrier-free mode — `BARRIER_FREE_MODE` ✅ *(not used by PWA)*
Simple u8 flag (the PWA source hints it's an accessibility/theme option).
```
Get:      [0x03, 0x00]           → [0x03, 0x80, 0x00, value]
Set:      [0x03, 0x01, value]    → [0x03, 0x81, 0x00]     value: 0 or 1
```
✅ Live: round-tripped 0 → 1 → 0.

#### DC bypass threshold — `DC_BYPASS_THRESHOLD` ✅ *(not used by PWA)*
Threshold voltage as a 16-bit SFLOAT (§6), little-endian.
```
Get:      [0x15, 0x00]                 → [0x15, 0x80, 0x00, sfloat_lo, sfloat_hi]
Set:      [0x15, 0x01, sfloat_lo, sfloat_hi] → [0x15, 0x81, 0x00]
```
✅ Live: read `d0 e7` = 20.00 V; set accepted and persisted.

#### Device ID — `DEVICE_ID` ✅ *(not used by PWA)*
```
Request:  [0x10, 0x00]
Reply:    [0x10, 0x80, 0x00, b0..b5]   6 bytes — the Bluetooth MAC, reversed byte order
```
✅ Live: returned `2b 72 eb 5a 04 dc` = MAC `DC:04:5A:EB:72:2B` (matches the address macOS reports).

#### USB-IC firmware version — `GET_USB_FW_VERSION` ✅ *(not used by PWA)*
```
Request:  [0x17, 0x00]
Reply:    [0x17, 0x80, 0x00, ...payload]
```
✅ Live: payload `01 00 00 05` (reads like version 1.0.0 + a 4th component/build of 5; exact field
meaning unconfirmed).

#### Reboot — `RESTART` ✅
```
Request:  [0x11, 0x01]      no reply — the write itself errors with a disconnect
```
✅ Live: the BLE link drops within ~1 s of the write (the write "fails" with a disconnection — that's the
success signal); device re-advertises in ~15 s. Settings (power limits, threshold, barrier-free) persist
across the reboot.

#### Running mode — `RUNNING_MODE_CONTROL` ✅
```
Request:  [0xE0, 0x01, mode]   mode: 0x00 user, 0x01 factory
Reply:    [0xE0, 0x81, 0x00]
```
✅ Live — **correction:** the device *does* reply (the PWA just never reads it). Factory mode was entered
and exited without observable telemetry changes, and it does **not** unlock the `0xFD`-gated opcodes
(`0x05`/`0x12`/`0xF0`).

#### Feature bitmask — `FEATURES` ✅
```
Request:  [0xFE, 0x00]
Reply:    [0xFE, 0x80, 0x00, feat32_le...]   uint32 little-endian at byte 3
```
✅ Live: reply `fe 80 00 ff 7f 00 00` → `0x00007FFF` (all 15 documented flags set on LP2_V5).
Feature flags (bit → capability):

| Bit | Flag | Meaning |
|---|---|---|
| 0 | FF_DISPLAY | Has display |
| 1 | FF_FACTORY_MODE | Supports factory mode |
| 2 | FF_SLEEP | Low-power sleep |
| 3 | FF_SHUTDOWN | Supports shutdown |
| 4 | FF_BATTERY_CAPACITY | Reports battery capacity |
| 5 | FF_DC_OUT_PORT | Has DC output port |
| 6 | FF_DC_OUT_CONTROL | DC output on/off control |
| 7 | FF_DC_OUT_SCHEDULER | DC scheduled on/off |
| 8 | FF_USB_PORT | Has USB port |
| 9 | FF_USB_POWER_LIMIT | USB power-limit control |
| 10 | FF_USB_OUTPUT_CONTROL | USB output on/off control |
| 11 | FF_DC_BYPASS | DC bypass supported |
| 12 | FF_DC_BYPASS_CONTROL | DC bypass toggle |
| 13 | FF_USB_DC_INPUT | USB-C supports DC input |
| 14 | FF_USB_DC_INPUT_POWER | USB DC-input power reporting |

#### Scheduled on/off timers — `SCHEDULED_ON_OFF` ✅
This command multiplexes CRUD via a **sub-opcode in byte 2**:

```
List timer IDs:     [0x06, 0x00, 0x00]
  Reply: byte2 RESULT, byte3 count, byte[4+i] = id for each timer

Get one timer:      [0x06, 0x00, 0x01, id]
  Reply: byte2 RESULT, then TIMER_SETTINGS struct starting at byte 4 (see below)

Add / edit timer:   [0x06, 0x01, 0x02, id, ...TIMER_SETTINGS]
  id = 0xFF to add new; reply byte3 = newly-assigned id
  Reply: byte2 RESULT

Delete timer:       [0x06, 0x01, 0x04, id]
  Reply: byte2 RESULT
```
✅ Live: full add → get → list → delete cycle confirmed on fw 1.4.9. Observed extras beyond the doc:

- **Get reply carries a 5-byte trailer** after the 9-byte struct. For a daily-03:00 timer created at
  2026-07-14: `... [struct] 01 30 72 e2 02`. Best guess (unconfirmed): flag byte + u32 LE next-fire
  timestamp — `0x02E27230` = 48 394 800 s is *exactly* 2026-07-15 03:00:00 if counted from
  2025-01-01 00:00:00. Treat as informational.
- **List reply had one extra byte after the id list** (`06 80 00 01 00 10` for one timer id 0 — trailing
  `0x10` unexplained; possibly a per-timer flags byte).
- Add with `id=0xFF` assigned id `0` on an empty table; reply `06 81 00 00` (byte 3 = new id) as documented.

`TIMER_SETTINGS` (9 bytes) — offsets are relative to the struct start:
```
off 0  int8   status   0=empty, 1=enabled, -1=disabled, -2=disabled(validation), -3=disabled(expired)
off 1  uint8  type     0=one-shot, 1=daily, 2=weekly, 3=monthly
off 2  uint8  hour     0..23
off 3  uint8  minute   0..59
off 4  (4 bytes) repeat, interpreted by type:
         type 0 (one-shot): year u16 LE (@4), month u8 (@6, 1..12), day u8 (@7, 1..31)
         type 1 (daily):    0,0,0,0
         type 2 (weekly):   weekdays u8 (@4), then 0,0,0    bit1=Mon … bit7=Sun (BLE Day-of-Week)
         type 3 (monthly):  monthdays u32 LE (@4)           bit1=day1 … bit31=day31
off 8  uint8  action   0=turn off, 1=turn on
```

### 3.5 Non-command writes (magic values)

These are raw characteristic writes, not the CMD/ACTION frame:

| Action | Characteristic | Bytes | Meaning |
|---|---|---|---|
| Enter OTA mode ✅ | `0x4301` (OTA) | `[0x50, 0x4B]` (`"PK"`) | Reboot into firmware-update mode; device reappears as `PeakDo-OTA` |
| Shutdown ✅ | `0x4310` (FactoryMode) | `[0x46, 0x4D]` (`"FM"`) | Power the device off |

✅ Live: both confirmed. Like `RESTART`, each write "fails" with an immediate disconnection — that is the
success signal. `PK` → device re-advertises as `PeakDo-OTA` in bootloader mode (see §9; note the macOS
bonding caveat in §12). `FM` → device powers off completely (stops advertising); it must be turned back on
with its physical button.

---

## 4. Telemetry characteristics (read + notify)

Enable notifications to get live updates, or read once. All multi-byte numbers are **little-endian**.
Voltage/current/power/temperature/capacity fields use **BLE 16-bit SFLOAT** (see §6).
`status`: `0 = idle, 1 = charging, -1 = discharging` (the PWA remaps `-1` → `2`).

### 4.1 ExtBatteryInfo — `0x4303` (16 bytes)
```
off 0   uint8   enabled
off 1   int8    status
off 2   uint8   full        (1 = fully charged)
off 3   sfloat  maxCapacity (Wh)
off 5   sfloat  capacity    (Wh)
off 7   uint8   level       (%)
off 8   sfloat  voltage     (V)
off 10  sfloat  current     (A)
off 12  sfloat  power       (W)
off 14  uint16  remain      (minutes of runtime remaining)
```

### 4.2 DcPortStatus — `0x4304` (8–11 bytes) ✅
```
off 0   uint8   enabled
off 1   int8    status
off 2   sfloat  voltage (V)
off 4   sfloat  current (A)
off 6   sfloat  power   (W)
off 8   uint8   bypassOn   (present only if length >= 9)
off 9   uint8   ?          (observed 0x00 on fw 1.4.9 — not parsed by PWA)
off 10  uint8   ?          (observed 0x7F and 0x00 on fw 1.4.9 — dynamic, not parsed by PWA)
```
✅ Live (fw 1.4.9): frame is **11 bytes**; decode through offset 8 confirmed sane
(19.6 V / 2.75 mA / 0.054 W idle). The PWA (built for 1.1.1-era firmware) stops parsing at byte 8,
so offsets 9–10 are undocumented; byte 9 stayed `0x00`, byte 10 read `0x7F` initially and `0x00`
after a reboot — meaning unknown.

### 4.3 TypeCPortStatus — `0x4305` (10–13 bytes) ✅
```
off 0   uint8   enabled
off 1   int8    status
off 2   sfloat  voltage     (V)
off 4   sfloat  current     (A)
off 6   sfloat  power       (W)
off 8   sfloat  temperature (°C)
off 10  uint8   ?           (observed 0x00 — gap byte, not parsed by PWA)
off 11  uint8   mode        (present if length >= 12) 0=disabled,1=input,2=output,3=all
off 12  uint8   isDCInput   (present if length >= 13)
```
✅ Live (fw 1.4.9): 13-byte frame confirmed; temperature SFLOAT decoded to a sane 25.0 °C, `mode`
tracked `TYPEC_CONTROL` (3 → 1 → 3). `isDCInput` read `1` while output was disabled and idle, `0` otherwise.

---

## 5. Set device time (Current Time `0x2A2B`)

Write 10 bytes (mostly standard BLE Current Time, little-endian year):
```
off 0  uint16  year (LE)
off 2  uint8   month (1..12)
off 3  uint8   day (1..31)
off 4  uint8   hour
off 5  uint8   minute
off 6  uint8   second
off 7  uint8   day-of-week (1=Mon … 7=Sun)
off 8  uint8   fractions256 (1/256 s = ms / 3.90625)
off 9  uint8   adjust reason (0=manual, 1=external reference)
```

---

## 6. BLE SFLOAT (16-bit) decode/encode

The IEEE-11073 16-bit float used for all analog telemetry:
```
raw (uint16 LE):
  mantissa = raw & 0x0FFF   → sign-extend from 12 bits
  exponent = raw >> 12      → sign-extend from 4 bits
  value    = mantissa * 10^exponent
```
Special encodings: `0x07FF` NaN, `0x07FE` +Inf, `0x0802` -Inf.

---

## 7. Device identity: models, variants, CID

The **CID** (16-bit "chip id" / product-variant id) drives feature detection and firmware selection.
```
CID = (model << 8) | variant
```
Models: `LP1 = 0x01`, `LPP = 0x02`, `LP2 = 0x03`.

| Variant | CID (hex) | CID (dec) |
|---|---|---|
| LP1_V1 | 0x0101 | 257 |
| LP1_V5 | 0x0102 | 258 |
| LP1_V5_1 | 0x0103 | 259 |
| LP1_V6 | 0x0104 | 260 |
| LPP_V1 | 0x0201 | 513 |
| LP2_V1 | 0x0301 | 769 |
| LP2_V3 | 0x0303 | 771 |
| LP2_V4 | 0x0304 | 772 |
| LP2_V5 | 0x0305 | 773 |

> ✅ **LP2_V5 (`0x0305`) observed live** on a `BP4SL3V2` with hw rev `V5#0305` — this variant is *not* in
> the PWA 1.1.1 `js_variants.js` table (which stops at LP2_V4); newer hardware exists beyond the app's map.
> Note this unit's hw-rev string had no `#rREV` suffix.

CID is obtained from the OTA `INFO` response (§8), or parsed from the Hardware Revision string
(`variant#CID[#rREV]`, e.g. `V6#0104#r3`), or inferred from the model number for legacy units.

Known model numbers: `BP4SL3V1` / `PK-LINK-POWER-1` → LP1, `BP4SL3` → LPP, `BP4SL3V2` → LP2.

---

## 8. Connection handshake (what the PWA does on connect)

1. `gatt.connect()`, then wait ~2 s.
2. `getPrimaryServices()` (prime the cache).
3. Write `[0x84]` (OTA `INFO`) to `0x4301`, read reply → parse `OTAInfo` (determines **app** vs **OTA** mode + CID).
4. Read Device Information strings (model, hardware rev, software rev, firmware rev).
5. **If app mode** (`mode == 1`):
   - Read + subscribe `DcPortStatus` (`0x4304`).
   - If LP1/LP2: read + subscribe `ExtBatteryInfo` (`0x4303`) and `TypeCPortStatus` (`0x4305`).
   - `FEATURES` query (`[0xFE,0x00]`).
   - Sync time (write `0x2A2B`).
   - Load timers (`SCHEDULED_ON_OFF` list + per-id get).
   - Check firmware update (§10).
6. **If OTA mode** (`mode == 2`): jump into the OTA update flow (§9).

---

## 9. OTA firmware update protocol (characteristic `0x4301`)

`writeAndRead` pattern (write then read reply). OTA opcodes:

| Name | Opcode |
|---|---|
| PROGRAM | `0x80` |
| ERASE | `0x81` |
| VERIFY | `0x82` |
| END | `0x83` |
| INFO | `0x84` |
| WHOLE_VERIFY | `0x85` |
| DETECT_MTU | `0x89` |
| PROGRAM_V2 (large chunk) | `0xA0` |
| FEATURES | `0x90` |

### 9.1 INFO — `0x84` ✅ (app mode)
Reply `OTAInfo`:
```
off 0  uint8  mode         1 = app, 2 = OTA(bootloader)
  If mode == 2:
off 1  uint32 otaStartAddress (LE)
off 5  uint16 blockSize (LE)
off 7  uint16 chipTypeId (LE)
off 9  uint32 appStartAddress (LE)
  Common (if present):
off 13 uint16 cid (LE)      (0 → treat as null)
off 15 uint8  revision
```
✅ Live (app mode): 15-byte reply `01 000000000000000000000000 05 03` — mode=1, offsets 1–12 all zero,
CID `0x0305` at offset 13. No revision byte in app mode (frame ends at 15 bytes).

✅ Live (bootloader mode, OTA 2.0.2): 20-byte reply `02 00100000 0010 8300 00000400 0503 01 00000000` —
mode=2, otaStartAddress `0x00001000`, blockSize `4096`, chipTypeId `0x0083`, appStartAddress `0x00040000`,
cid `0x0305`, revision `1`, plus **4 undocumented trailing zero bytes**.

Bootloader GATT map is minimal: service `0x5301` exposes **only** the OTA characteristic `0x4301`
(no command/telemetry chars), plus Device Information `0x180A` with the same strings as app mode **and**
Manufacturer Name `0x2A29` (`"PeakDo"`, absent in app mode).

### 9.2 FEATURES — `0x90` ✅
Reply `[0x90, 0x04, feat32_le]`. Flags: `bit0 DETECT_MTU`, `bit1 WHOLE_VERIFY`, `bit2 LARGE_CHUNK`.

✅ Live (bootloader 2.0.2): `90 04 07 00 00 00` → `0x7` = all three features supported.

### 9.3 DETECT_MTU — `0x89` ✅
Binary-search the max frame size. Send a packet of length `mtu` = `n*16 + headSize`:
```
[0x89, mtu&0xFF, (mtu>>8)&0xFF, 0xFF … pad to mtu bytes]
Reply: uint16 LE at byte 3 = frame length the device actually received.
```
✅ Live: 67-byte probe → `89 43 00 43 00` (request length echoed at bytes 1–2, received length 67 at byte 3).

### 9.4 ERASE — `0x81`
```
[0x81, 0x04, (start>>4)&0xFF, ((start>>4)>>8)&0xFF, blocks&0xFF, (blocks>>8)&0xFF]
blocks = ceil(fwSize / blockSize)
Reply: byte0 == 0 on success.
```

### 9.5 PROGRAM — `0x80` / PROGRAM_V2 — `0xA0`
Addresses are `>>4` (16-byte granularity); data chunks aligned to 4 bytes, tail padded with `0xFF`.
```
Small chunk (0x80):  [0x80, alignedSize_u8, (off>>4)&0xFF, ((off>>4)>>8)&0xFF, ...data]
Large chunk (0xA0):  [0xA0, alignedSize&0xFF, (alignedSize>>8)&0xFF,
                      (off>>4)&0xFF, ((off>>4)>>8)&0xFF, 0,0,0, ...data]
Reply: byte0 == 0 on success. (Sent write-without-response in "fast mode".)
```
Default chunk size when MTU detection unsupported: `16*15 = 240` data bytes.

### 9.6 VERIFY — `0x82` (per-chunk) or WHOLE_VERIFY — `0x85`
```
WHOLE_VERIFY: [0x85, 0x0D, (start>>4)&0xFF, (start>>12)&0xFF,
               fwSize_le32, crc32_le32, verMajor, verMinor, verPatch]
Reply: [0x85, 0x00] on success.
```
Per-chunk VERIFY mirrors PROGRAM (same address/data layout, opcode `0x82`).

### 9.7 END — `0x83` ✅ (as bootloader exit)
```
If WHOLE_VERIFY supported: [0x83]
Else:  [0x83, 0x0D, (start>>4)&0xFF, (start>>12)&0xFF,
        fwSize_le32, crc32_le32, verMajor, verMinor, verPatch]
```
For OTA bootloader ≥ 1.1.x the device returns `0` on the OTA char to confirm exit.

✅ Live (bootloader 2.0.2): a bare `[0x83]` with no prior erase/program cleanly exits the bootloader —
the reply read came back **empty** (0 bytes, not a `0x00` byte), the link dropped ~2 s later, and the
device rebooted into app mode advertising `Link-Power-2`. This is the recovery path if you enter OTA mode
by accident. (ERASE/PROGRAM/VERIFY were deliberately not exercised.)

---

## 10. Firmware CDN — HTTP API (`https://api.peakdo.ca/fw-api`)

The **only** HTTP API. Read-only, no auth. CORS `*`. Base URL: `https://api.peakdo.ca/fw-api`.

### 10.1 Check latest firmware
```
GET /channel/{channel}/cid/{cid}/fw/latest?ver={a.b.c}
GET /cid/{cid}/fw/latest?ver={a.b.c}        (default channel)
```
- `channel`: `0 = test`, `1 = release`.
- `cid`: **decimal** of the 16-bit CID (e.g. `769` for LP2_V1 `0x0301`).
- `ver`: current firmware version; the `_suffix` (if any) is stripped before sending.

Response (verified live):
```json
{
  "data": [
    {
      "fid": 19, "channel": 1, "type": 1,
      "major": 1, "minor": 4, "patch": 8,
      "log": "- [FIXED] scheduled DC On/Off not working\n- …",
      "buildAt": "2026-01-31T11:16:00",
      "publishAt": "2026-02-02T15:50:22",
      "cid": 769
    }
  ],
  "cid": 769, "channel": 1, "version": "1.0.0"
}
```
`data` is empty (`[]`) when no newer firmware exists. `type`: `1 = app`, `2 = ota`.

### 10.2 Download firmware binary
```
GET /fw/{fid}/bin
```
Returns `application/octet-stream` — a **`.fwp` firmware pack** (§11). `fid` is from the check response.

---

## 11. `.fwp` firmware-pack format

Little-endian throughout. Used to wrap an Intel-HEX firmware image with metadata + compatibility list.

```
FP header:
  u16  magic  = 0x4650 ('FP')
  u16  version = 1
  16 bytes reserved (0x00)

FW section (64-byte header + payload):
  u16  magic  = 0x4657 ('FW')
  u16  structVersion = 2
  u8   type          (1 = app)
  u8   verMajor, u8 verMinor, u8 verPatch
  u32  start          (flash start address)
  u32  length         (firmware byte length)
  u32  crc32          (IEEE CRC32 over firmware bytes)
  u32  buildTime      (minutes since Unix epoch)
  38 bytes reserved (0x00)
  u16  crc16          (CCITT-FALSE over the preceding 62 header bytes)
  ... firmware bytes (length) ...

CL section (compatibility list):
  u16  magic = 0x434C ('CL')
  u16  count
  16 bytes reserved
  count × u16 cid    (CIDs this firmware supports)

LOG section (changelog):
  u16  magic = 0x4348 ('CH')
  u16  length         (includes trailing NUL)
  bytes  UTF-8 text, NUL-terminated

Trailer:
  u32  crc32          (IEEE CRC32 over the entire pack up to here)
```

### 11.1 CRC algorithms
- **CRC-16/CCITT-FALSE**: poly `0x1021`, init `0xFFFF`, no input/output reflection, no xor-out.
- **CRC-32/ISO-HDLC (IEEE 802.3)**: poly `0xEDB88320` (reflected), init `0xFFFFFFFF`, xor-out `0xFFFFFFFF`.

---

## 12. Implementation notes for Swift / CLI

- **Swift:** use `CoreBluetooth`. Scan for peripherals whose name starts with `Link-Power`; connect;
  `discoverServices([CBUUID(string: "5301"), CBUUID(string: "180A"), CBUUID(string: "1805")])`.
  16-bit UUIDs work directly with `CBUUID(string:)`. For the command channel, write to `0x4302` with
  `.withResponse`, then `readValue` and parse in the `didUpdateValueFor` callback. Subscribe
  (`setNotifyValue(true,…)`) on `0x4303/0x4304/0x4305` for live telemetry.
- **CLI/library:** Python `bleak`, Node `@abandonware/noble`, or Rust `btleplug` all work. The whole protocol
  is write-then-read on `0x4302` plus notifications on the three status characteristics — no pairing PIN is
  required to read/write unless the device has one set (see `BLE_PIN`).
- Decode all analog values with the SFLOAT routine in §6, not standard IEEE-754.
- `status == -1` means discharging; watch the signed `int8`.
- Only LP1/LP2 expose ExtBatteryInfo + TypeCPortStatus; gate reads on model/CID or the FEATURES bitmask.
- The pack/CRC and OTA sections are only needed if you implement firmware updates — controlling the device
  does not require them.
- **macOS/CoreBluetooth bonding trap (hit live):** the app firmware requests encryption on connect, so
  macOS silently pairs and stores an LE bond. The **bootloader has no bond storage**, so after entering OTA
  mode every connect fails with `CBErrorDomain Code=14 "Peer removed pairing information"`. Fix: while the
  device is in bootloader mode, forget it in System Settings → Bluetooth (the CLI routes —
  `blueutil --unpair`, editing `/Library/Bluetooth/com.apple.MobileBluetooth.ledevices.paired.db` — did not
  stick). Reconnecting to the app afterwards silently re-pairs, re-arming the trap for the next OTA entry.
- **Stale name caching:** `CBPeripheral.name` (bleak's `device.name`) can lag reality — it kept reporting
  `Link-Power-2` while the device was advertising `PeakDo-OTA`. Always match on the advertisement's
  fresh `local_name`, not the cached peripheral name.

---

## 13. Source files (for cross-reference)

Downloaded to `src/` in this project (from `https://pwa.peakdo.ca/link-power-1/…?v=1.1.1`):

| File | Contents |
|---|---|
| `js_ble.js` | GATT UUIDs, BLE manager/device wrappers |
| `js_lp-ble-cmds.js` | Command / action / OTA / running-mode enums |
| `views_main-view.js` | All control commands + telemetry parsers + handshake |
| `views_ota-view.js` | Full OTA update state machine |
| `js_utils.js` | SFLOAT decode/encode, helpers |
| `js_feature-set.js` | Feature-flag bitmask |
| `js_variants.js` | Models + variant → CID map |
| `js_timer.js` | Timer struct (de)serialization |
| `js_ota-types.js` | OTAInfo parsing |
| `js_ota.js` | Firmware CDN HTTP client |
| `js_fw-pack.js` | `.fwp` pack/parse |
| `js_crc.js` | CRC-16 / CRC-32 |
| `stores_main.js` | State model, `fwApiBaseUrl`, feature inference |
