# Continue — GL-X3000 Link-Power pairing  ✅ RESOLVED

## Outcome (2026-07-16)

Pairing works end to end. On the GL-X3000 (BlueZ 5.64, RTL8761B), the daemon
now bonds the Link-Power, stores a long-term key, and runs telemetry:

- `POST /api/v1/pairing/pair {mac, pin:"020555"}` → stage `pairing` → `paired`
- `/api/v1/status` → `"connected": true` (BP4SL3V2, fw 1.4.9)
- `[LongTermKey]` persisted in `/etc/bluetooth/keys/<adapter>/DC:04:5A:EB:72:2B/info`,
  `Trusted=true`; `device_mac`/`pin` saved to UCI
- Protected `0x4301` write returns Write Response (success); full telemetry flows

wattline 1.1.0 (with the GUI pairing flow) is installed and running on the router.

## Root cause

The device requires **authenticated LE pairing** (Passkey Entry — it holds the
fixed passkey 020555 = numeric 20555; NOT Just Works, contrary to the old
API.md note). SMP is only triggered by an explicit pair or a protected
operation; BlueZ does **not** auto-elevate security on the device's
`Insufficient Authentication` response the way macOS/CoreBluetooth does. The
pre-1.1.0 daemon registered a pairing agent but never called
`org.bluez.Device1.Pair`, so SMP never ran and no LTK was ever stored — hence
the transient `Paired: yes` with nothing persisted.

## Fix (validated on hardware)

The 1.1.0 pairing flow supplies exactly what was missing: discover the device
fresh → `Device1.Pair` (no prior `connect`) → the BlueZ agent answers
`RequestPasskey` with 20555 → `Trust` → wait for the connector to reconnect and
survive the protected handshake before reporting success. btmon confirmed the
SMP exchange: initiator `Bonding, MITM, SC` (0x2d), device responds `Bonding,
MITM, Legacy` (0x05, downgrades to legacy), passkey `0x504b`, Encryption
Change, New Long Term Key stored. KeyboardOnly agent capability works.

Bond storage note: on GL firmware `/var/lib/bluetooth` is a symlink to
`/etc/bluetooth/keys/` (persistent flash); it is writable and persists bonds
correctly. A `bluetoothctl remove` does not always delete the flash copy —
`rm -rf` the device dir for a truly clean re-pair test.

## If it regresses

- Recheck the stored bond: `cat /etc/bluetooth/keys/*/DC:04:5A:EB:72:2B/info`
  (needs `[LongTermKey]`).
- Re-pair from the GUI (GL panel or LuCI → Wattline) or the API
  (`/api/v1/pairing/scan` then `/pairing/pair`).
- The device accepts one BLE central at a time — forget it on any phone/laptop
  before router pairing (macOS re-bonds whenever the PWA connects).
- Do not factory-reset the device or change the 5.4.211 kernel / RTL8761B
  modules without explicit approval.
