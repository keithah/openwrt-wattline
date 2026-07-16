# Continue — GL-X3000 Link-Power pairing

## Last action

Installed and verified the Wattline 1.0.0 packages on the GL-X3000, then tried to pair `Link-Power-2` (`DC:04:5A:EB:72:2B`) with fixed PIN `020555`. BlueZ temporarily reports `Paired: yes`, but `/var/lib/bluetooth/<adapter>/<device>/info` never gains a `[LongTermKey]` section and `Paired` returns to `no` on disconnect. The protected `0x4301` write therefore fails authentication. `wattlined` is intentionally stopped and the router has no active BLE connection; `bluetoothd` and `hci0` remain healthy.

## Next action

From the laptop with Chrome access, capture a successful browser/PWA pairing flow (preferably alongside `btmon` on the router), compare its SMP/authentication sequence with the router, then design the daemon API and GL/LuCI UI flow for scan → select device → pair with PIN → trust → connect. The daemon currently registers a BlueZ agent but never calls `org.bluez.Device1.Pair` itself.

## Why

The dongle, firmware, BlueZ, scanning, device discovery, and ordinary BLE connection all work. The remaining failure is specifically authenticated bonding: a connection alone is insufficient for the Link-Power's protected GATT characteristics.

## Open threads

- GUI pairing is required in both the native GL panel and/or LuCI; choose the product surface during design.
- Pairing attempts exposed a stale BlueZ request pattern: the first `pair` can hang; cancelling it and immediately pairing again reports success, but still did not produce an LTK in this environment.
- The package build now normalizes IPK ownership/modes and runs `package/check-ipk-metadata.sh`; this fixed a `umask 0077` bug that had installed shared router directories as UID 1000/mode 0700.
- The Link-Power/browser bond may persist on the peripheral after the browser OS forgets the device. Do not assume an OS-side forget cleared peripheral state.

## Do not

- Do not treat `Connected: yes` or transient `Paired: yes` as success. Require a stored LTK, a successful protected `0x4301` write, and `/api/v1/status` showing `"connected":true`.
- Do not factory-reset the Link-Power without explicit approval.
- Do not upgrade GL firmware or change the 5.4.211 kernel; the custom RTL8761B modules depend on that ABI.
- Do not leave a manual scanner or browser connected while testing Wattline; the Link-Power accepts one BLE client at a time.
