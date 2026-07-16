# Continue — GL-X3000 Link-Power pairing

## Last action (2026-07-16, laptop session)

Implemented the full GUI pairing flow (v1.1.0, uncommitted in worktree
`wattline-ble-pairing-auth-757886`): async pairing state machine
(`internal/ble/pairing.go`), BlueZ D-Bus scan/pair/trust/unpair
(`internal/ble/bluez.go`), four authed endpoints under `/api/v1/pairing/*`,
connector Pause/Resume (race-safe across in-flight dials), runtime agent-PIN
override with rollback, UCI persistence, and pairing UI in both the GL panel
and LuCI. Pairing success is verified — the manager waits for the connector to
reconnect and survive the protected handshake before persisting or reporting
`paired` (see `docs/pairing-design.md`). An 8-angle code review found and
fixed 10 real bugs, including a transport leak on handshake failure and a
PIN→UCI injection. `go test ./...`, linux/arm64 build, and `make all` (IPKs
1.1.0, now needs GNU tar — auto-resolved) all pass.

## Root cause status (LTK bug) — still open, evidence staged

- The target router `gl-x3000` (192.168.8.1 = Tailscale **100.100.17.99**) has
  been offline since ~09:00 on 2026-07-16. **`gl-x3000-1` is a different
  router** (rejects the default key). A monitor + a ready-made btmon pairing
  experiment script exist in the session scratchpad
  (`router-pair-experiment.sh`: btmon -w + scripted bluetoothctl pair + bond
  dump; PIN as $1).
- Link-Power-2 is physically near the MacBook; macOS holds an LE bond and was
  auto-connected at session start. **Leading hypothesis:** the peripheral's
  bond slot is occupied by the Mac, so router pairing negotiates non-bonding
  (matches transient `Paired: yes`, store_hint=0, no LTK). **Secondary:** the
  2026-07-14 live test sent `BLE_PIN SET 000000`, so the device PIN may be
  000000 rather than 020555.
- The PWA does no SMP — "browser pairing" is macOS CoreBluetooth silently
  pairing (likely Just Works). A Web Bluetooth capture harness is set up in
  Chrome (button "WL CAPTURE" on pwa.peakdo.ca; logs to `window.__wl`) but
  needs Keith to click it and pick the device in the chooser (synthetic
  clicks can't drive the chooser).

## Next action

1. When Keith clicks WL CAPTURE: read `window.__wl` — confirm the protected
   `0x4301 [0x84]` write works from the Mac's bond and note whether macOS
   re-pairs.
2. When `gl-x3000` comes back: free the device (disconnect/unpair the Mac if
   needed — `blueutil` installed), install the 1.1.0 IPKs, run the btmon
   experiment with PIN 020555 then 000000, and read the SMP pairing
   request/response AuthReq flags + key distribution to pin the root cause.
3. Fix per evidence (bond slot vs PIN vs RTL8761B/SC quirk), then validate the
   full GUI flow end-to-end: scan → pair → stored `[LongTermKey]` →
   `/api/v1/status` `"connected": true`.

## Do not

- Do not treat `Connected: yes` or transient `Paired: yes` as success. Require
  a stored LTK, a successful protected `0x4301` write, and `/api/v1/status`
  showing `"connected": true` (the daemon's pair flow now enforces this).
- Do not factory-reset the Link-Power without explicit approval.
- Do not upgrade GL firmware or change the 5.4.211 kernel; the custom RTL8761B
  modules depend on that ABI.
- Do not leave a manual scanner or browser connected while testing Wattline;
  the Link-Power accepts one BLE client at a time.
