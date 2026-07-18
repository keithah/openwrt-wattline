# GL-X3000 and real-BLE verification checklist

This checklist is the release gate for behavior that host-side tests cannot
prove. Run it on a GL-X3000 with the intended firmware/kernel, a working BLE
adapter, and a Link-Power. Start with `wan_access=0`. Replace example addresses,
tokens, timer IDs, and interface names with the test installation's values.

Every item below is intentionally unclaimed. Record date, router firmware,
kernel, package version, Link-Power firmware, and evidence when changing an item
to PASS or FAIL.

## Install, reboot, and credentials

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Transfer all five IPKs, inspect
  them with `tar tzf`, then install `wattline-bt`, `wattlined`,
  `luci-app-wattline`, and `gl-app-wattline`. Inspect USB sysfs and install
  `wattline-rtl8761b` only for `2357:0604` or `0bda:8771`. Expected: opkg accepts
  the gzip ustar archives and reports matching package versions/architectures.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Before installing the optional
  driver, record hashes of `/lib/modules/5.4.211/{btintel,btrtl,btusb}.ko` when
  present and `/etc/modules.d/bluetooth`. Install it, then run `driverctl status`,
  `modinfo`, `hciconfig -a`, and inspect `dmesg`. Expected: status is `packaged`,
  vermagic is exact, firmware uploads, and `hci0` is UP without memory errors.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Remove and reinstall
  `wattline-rtl8761b`. Expected: removal restores the recorded stock files and
  autoload list, reinstall preserves the first backup and reactivates cleanly.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Run
  `stat -c '%a %n' /etc/wattline /etc/wattline/tls /etc/wattline/tokens.json /etc/wattline/tls/server.key`
  and `uci -q get wattline.main.token`. Expected: directories are `700`, secret
  files are `600`, and a nonempty bootstrap token exists without appearing in
  daemon logs.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Reboot with the RTL8761B
  package installed, then inspect init links, module hashes/order, firmware log,
  `hci0`, and wattlined. Expected: the driver service runs at S15 before
  wattlined at S95, the packaged hashes persist, and BLE scanning still works.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Run
  `/etc/init.d/wattlined enable; reboot`, reconnect, then inspect
  `/etc/init.d/wattlined status` and `logread -e wattline`. Expected: procd starts
  the daemon once, it survives reboot, and no credential is regenerated.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Reinstall the same-version IPKs
  using `opkg install --force-reinstall /tmp/*.ipk`. Expected: the binary updates
  while the bootstrap token, managed tokens, certificate, UCI rules, and paired
  BLE MAC remain unchanged.

## BLE adapter, pairing, and identity handshake

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Confirm the adapter with
  `hciconfig -a` and `bluetoothctl list`, then call `POST /api/v1/pairing/scan`
  and poll `GET /api/v1/pairing/status`. Expected: asynchronous stages advance
  from scanning to idle and discovered entries include MAC, name, RSSI, and
  paired state.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Pair the target using
  `POST /api/v1/pairing/pair` with BLE PIN `020555`, then poll pairing status and
  `GET /api/v1/device`. Expected: BlueZ pairs/trusts it, UCI preserves the MAC
  and PIN, connection reaches `ready`, and concurrent scan/pair returns
  `409 operation_in_progress`.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Compare `/api/v1/device` with
  the physical unit and protocol reads. Expected: reversed MAC, model, hardware
  variant, application firmware, OTA bootloader firmware, CID, raw FEATURES,
  decoded capabilities, characteristic availability, app/OTA mode, connection,
  and pending/recent commands are accurate.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Remove with
  `DELETE /api/v1/pairing/device/{mac}` and pair again through each UI. Expected:
  unpair is reflected by BlueZ and both panels can restore the ready connection.

## Granular controls and telemetry truth

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Call
  `POST /api/v1/device/dc` with `on` false then true while capturing BLE traffic.
  Expected: frames are `01 01 00` and `01 01 01`; replies confirm only after
  `DcPortStatus.enabled` changes, and SSE publishes pending then confirmed state.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Call
  `POST /api/v1/device/usbc/output` off then on. Expected: frames are
  `13 01 02 00` and `13 01 02 01`; off confirms at Type-C MODE 1 and on at MODE
  3, regardless of the telemetry `enabled` field.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — GET global/input/output/runtime
  USB-C limits. PUT each settable type through 30/45/60/65/100/140 W and DELETE
  it. Expected: levels map 0..5, SET uses `02 01 type level`, clear uses
  `02 02 type` (never `06`), mutations re-GET the device value, runtime rejects
  mutation, and only runtime renders `ff` as `level:-1, watts:null`.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Toggle DC bypass and capture
  frame `14 01 op`. Expected: the nonstandard result byte is ignored, only
  `DcPortStatus.bypassOn` confirms, and withholding telemetry yields pending then
  `504 command_timeout` at ten seconds.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — GET and PUT the bypass threshold
  across representative voltages. Expected: opcode `15` SFLOAT encoding matches
  the protocol and the PUT response is the re-read device value.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Watch
  `GET /api/v1/events` during all controls. Expected: the initial and subsequent
  events use `data: JSON` followed by a blank line, contain complete telemetry,
  identity, and command lists, and telemetry—not request echo—is authoritative.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Start two mutations
  concurrently. Expected: BLE captures show exactly one transaction in flight
  with no SET/GET or timer-list interleaving.

## Clock and timers

- [ ] **NOT RUN — requires GL-X3000/real BLE** — On a device exposing readable
  Current Time, call clock GET and sync. Expected: GET reads `0x2A2B`, reports
  device/system time and drift; sync writes the exact ten-byte Current Time with
  adjustment reason 0. Reconnect handshake continues using reason 1.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — On a device/inventory without a
  readable Current Time characteristic, call clock GET. Expected:
  `available:false`, null device time/drift, and zero BLE transport I/O.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Create one-shot, daily, weekly,
  and monthly timers via canonical POST. Expected: ADD uses ID `ff`, adopts the
  reply ID, uses the exact nine-byte TIMER_SETTINGS representation, and re-lists
  authoritatively; weekly bits 1..7 mean Monday..Sunday and monthly bits 1..31
  mean days 1..31.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — GET, PUT, and DELETE each timer,
  then reboot both device and router. Expected: returned lists match device
  storage, one-shot dates survive, writable status is 1/-1, and device-rendered
  -2/-3 states remain readable but cannot be submitted.

## Restart, shutdown, and OTA lifecycle

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Call restart and capture the
  disconnect. Expected: opcode `11`; disconnect or a write error after disconnect
  counts as success, reconnect remains armed, and the ready connection returns
  after approximately 15 seconds.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Call shutdown first without,
  then with, `{"confirm":true}`. Expected: the first is 400; the second writes
  `FM` (`43 10`), expected disconnect counts as success, and reconnect disarms.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — GET OTA INFO, enter OTA with
  confirmation, GET INFO in bootloader, then exit. Expected: INFO reports mode,
  CID, and bootloader version; enter writes `PK` (`43 01`); expected disconnects
  are successes; reconnect matches the fresh `PeakDo-OTA` advertisement, keeps
  the stable app-mode device ID, then exits and returns to app mode.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Inspect every UI and route list.
  Expected: there is no firmware download, upload, erase, program, verify, or
  flash operation and no rejected `05`, `12`, or `f0` command route.

## Advanced controls

- [ ] **NOT RUN — requires GL-X3000/real BLE** — With `advanced=0`, call every
  advanced operation on capable hardware. Expected: `403 advanced_disabled`;
  unsupported hardware takes precedence with `409 capability_unsupported`.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Enable advanced mode and PUT
  running mode 0/1. Expected: RUNNING_MODE_CONTROL uses opcode `e0`; response
  handling accepts the device's `e0 81 00` read form.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — GET/PUT barrier-free, GET USB
  firmware version, and PUT BLE PIN `020555`. Expected: opcodes `03`, `17`, and
  SET-only `04` respectively; BLE PIN uses uint32 little-endian and is persisted
  only after device success.

## HTTP, HTTPS, IPv4, IPv6, and pinning

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Curl authenticated HTTP and
  HTTPS endpoints through LAN IPv4 and bracketed LAN IPv6. Expected: enabled
  listeners answer on 8377/8378 on both families and disabled listeners refuse
  connection after the required service restart.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Compute
  `openssl x509 -in /etc/wattline/tls/server.crt -outform DER | sha256sum` and
  compare settings, pairing response, QR, and mDNS. Expected: all publish the
  same 64-character lowercase DER SHA-256 served by HTTPS.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Rotate TLS with confirmation.
  Expected: new files are atomically durable, the response says restart is
  required, the active listener retains its old certificate until restart, and
  every published fingerprint changes to the served certificate after restart.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Pin the certificate in a test
  client, then rotate/restart. Expected: the old pin fails and the explicitly
  accepted new pin succeeds; plain HTTP remains available only when configured.

## mDNS and remote VPNs

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Browse `_wattline._tcp` from
  LAN using `avahi-browse -rt _wattline._tcp` or `dns-sd`. Expected: service
  appears only after stable MAC identity and TXT keys are exactly `ver`, `api`,
  `id`, `model`, `cid`, `features`, `tls`, `auth`, with documented formatting.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Browse from guest/WAN/tailscale
  and inspect packets with `tcpdump -ni any port 5353`. Expected: advertisements
  are confined to configured UP multicast LAN interfaces; no WAN or tunnel mDNS
  publication occurs, and service updates preserve an existing healthy record.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Reach HTTP and HTTPS using the
  router's Tailscale MagicDNS name and a saved client token. Expected: both work
  subject to Tailscale ACLs, the name appears in device/pairing metadata, and its
  device ID matches LAN mDNS. `tailscale serve` is not required.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Repeat through an ordinary
  WireGuard/VPN interface. Expected: all-address bind makes it reachable when
  the VPN firewall allows it, without enabling WAN access.

## Firewall policy

- [ ] **NOT RUN — requires GL-X3000/real BLE** — With `wan_access=0`, scan/curl
  both ports from the actual WAN side and inspect `uci show firewall | grep wattline`.
  Expected: connections are denied and no Wattline accept rules remain.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Set `wan_access=1`, commit, and
  reload. Expected: exact TCP rules appear only for enabled listener ports,
  firewall reload succeeds, and logs warn `insecure — use TLS/VPN`.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Disable one listener, change a
  port, simulate a firewall reload failure, then retry/reboot. Expected: rules
  reconcile to exact current policy, stale rules disappear, and the durable
  pending marker completes recovery rather than silently diverging.

## API-client pairing and token lifecycle

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Open pairing mode in each UI.
  Expected: a zero-padded six-digit PIN and QR appear only to admin, expire at
  the configured TTL, close on request, and wrong/expired/limited attempts all
  return indistinguishable `401 invalid_or_expired_pin`.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Decode the QR. Expected: exact
  `wattline://pair` parameter order and RFC 3986 escaping, preferred
  MagicDNS/LAN host, enabled ports only, no token/BLE PIN, and current TLS pin.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Exchange a PIN with a client
  label. Expected: one new `wlt_` secret is returned exactly once; token listing
  shows label/created/last-seen metadata but never secret/hash/private key.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Use the client token, observe
  coalesced last-seen, revoke it, and retry. Expected: client routes work before
  revocation; rule mutation and manual webhook actions return 403 without an
  outbound request; revocation is immediate; and the bootstrap token cannot be
  revoked.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Enable `pairing_always_on`, wait
  through a TTL, and disable it. Expected: enrollment stays open with rotating
  PINs while enabled and closes under normal explicit-mode policy afterward.

## UIs, automation, streaming, and persistence

- [ ] **NOT RUN — requires GL-X3000/real BLE** — Exercise LuCI identity,
  telemetry, all controls, BLE pairing, API pairing/QR, tokens, settings, TLS,
  and reachability. Expected: transport fallback never downgrades an explicitly
  pinned HTTPS connection and form errors preserve unsaved edits.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Repeat in the native GL panel.
  Expected: the same controls and administration work through oui RPC without
  exposing bootstrap credentials in page markup/logs and failed settings saves
  preserve edits.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Create/update/delete every rule
  condition and trigger representative DC/USB-C/restart/webhook actions.
  Expected: UCI round-trips hostile string characters safely, hysteresis/hold/
  repeat policy works, webhooks fire only as configured, and legacy action
  aliases remain compatible.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — input present → remove input → countdown reaches 10m → Link-Power shuts down →
  restore input → Link-Power wakes → GL-X3000 boots → wattlined reconnects →
  remove input again → full countdown starts again
  Expected: both dedicated Power-loss shutdown cards show the countdown and
  reset states; shutdown removes router power, hardware wake starts the router,
  and the second loss receives a fresh full hold rather than reusing blind time.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Keep an SSE client open while
  controls, telemetry, rules, reconnect, and token revocation occur. Expected:
  complete snapshots continue until disconnect/revocation and no cloud or
  off-box telemetry is emitted. Stall a second client beyond 128 queued
  snapshots; expected: only that slow stream ends and reconnect starts with a
  fresh complete snapshot without unbounded router memory growth.
- [ ] **NOT RUN — requires GL-X3000/real BLE** — Reboot after changing device
  pairing, timers, rules, listeners, TLS paths, token store, pairing policy,
  advanced mode, mDNS interfaces, and WAN policy. Expected: intended settings
  persist, procd/firewall/discovery agree with UCI, tokens remain valid except
  revoked ones, and the BLE connection recovers.
