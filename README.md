# openwrt-wattline

Monitor and automate a **PeakDo Link-Power** portable power station (LinkPower 2 /
LP1 / LP+) from an **OpenWrt / GL.iNet router**, over Bluetooth LE.

`wattlined` is a small static Go daemon that speaks the Link-Power's BLE (GATT)
protocol and runs configurable automation rules — *"shut down after 10 min of no
input power"*, low-battery webhooks, scheduled actions — with no cloud and no
account. It exposes a local HTTP API (telemetry, rules, manual control) and ships
two web UIs: a **native GL.iNet admin-panel app** (under Applications) and a
**LuCI** app.

Both UIs cover the full lifecycle:

- **Pairing** — scan, select the Link-Power, enter its PIN, and pair from the
  browser (authenticated BLE bonding; no SSH needed).
- **Monitoring** — battery, DC-port and USB-C telemetry with live updates.
- **Control** — DC / USB-C output and DC bypass toggles; USB-C output power
  limit (30–140 W); DC bypass engage-voltage threshold; on-device on/off
  schedules; and Restart / Power-off.
- **Automation** — the rules engine and webhooks.

See the [changelog](CHANGELOG.md) for version history.

## Screenshots

The native GL.iNet admin-panel app (**Applications → Wattline**). The LuCI app
mirrors the same layout.

| Pairing | Monitoring & control |
|---|---|
| <img src="docs/screenshots/gl-pairing.png" alt="Pairing screen: scan, select the Link-Power, enter PIN" width="360"> | <img src="docs/screenshots/gl-panel.png" alt="Connected panel: battery, DC/USB-C, device settings, schedules, power" width="360"> |

Built for the GL.iNet Spitz AX (GL-X3000) but portable to any aarch64 OpenWrt
target. The BLE protocol reference is in [`docs/API.md`](docs/API.md)
(reverse-engineered and live-verified).

> **Bluetooth adapter:** most GL routers have no onboard BLE — you need a USB
> dongle. Use a **CSR8510** adapter (TP-Link **UB400** / any "CSR 4.0" dongle):
> it works with the stock kernel `btusb`, no firmware needed. An **RTL8761B**
> dongle (TP-Link **UB500**) does **not** work out of the box on the GL-X3000's
> 5.4 kernel — see [`dongle-rtl8761b/`](dongle-rtl8761b/) if you're stuck with one.

## Building the OpenWrt packages

The packages are built without the full OpenWrt SDK — a plain Go
cross-compile plus a hand-rolled `.ipk`. An OpenWrt `.ipk` is a **gzipped
tar** of `debian-binary`, `control.tar.gz`, `data.tar.gz` (not an ar/.deb
archive — see the format note below).

```sh
make -C package all
ls -la package/out/*.ipk
```

This produces (the `<version>` in each filename is `VERSION` from
`package/Makefile`):

- `package/out/wattlined_<version>_aarch64_cortex-a53.ipk` — the daemon, procd
  init script, UCI defaults, and a uci-defaults token generator.
- `package/out/wattline-bt_<version>_all.ipk` — a metapackage pulling in
  `bluez-daemon`, `dbus`, `kmod-bluetooth` (the Bluetooth stack for a USB BLE
  dongle on routers without onboard BLE, e.g. the Spitz AX). `kmod-bluetooth`
  ships `btusb.ko`; there is no separate `kmod-btusb` in the GL feed.
- `package/out/luci-app-wattline_<version>_all.ipk` — the LuCI web UI (see below).
- `package/out/gl-app-wattline_<version>_all.ipk` — native GL.iNet admin-panel app
  (adds an entry under **Applications** in the GL UI; see Web UI below).

Prebuilt `.ipk`s and an opkg feed index are attached to each
[GitHub release](https://github.com/keithah/openwrt-wattline/releases) — see
[Install from a release](#install-from-a-release) below.

`ARCH` in `package/Makefile` targets `aarch64_cortex-a53` (GL.iNet Spitz AX /
similar aarch64 OpenWrt targets). Adjust it if you're packaging for a
different target.

### `.ipk` format (verified on-target)

Two things that only surface on the real router (GL-X3000, opkg 2021-06-13):

- The outer wrapper must be a **gzipped tar**, not an `ar`/.deb archive —
  opkg **segfaults** on ar-format ipks. The Makefile uses `tar czf`.
- Tar members must be **ustar** format (`--format ustar`); opkg rejects the
  pax extended headers macOS BSD tar emits by default (`Unknown typeflag
  0x78`). All package paths are well under ustar's 100-char limit.

When iterating on the router, note `opkg install` **skips a same-version
reinstall** — use `opkg install --force-reinstall` (or bump the version) to
load a rebuilt binary.

## Install order on the router

`wattlined` depends on `wattline-bt`, so install the Bluetooth metapackage
first:

```sh
# scp isn't available on OpenWrt dropbear; pipe over ssh instead
for f in package/out/*.ipk; do ssh root@192.168.8.1 "cat > /tmp/$(basename $f)" < "$f"; done
ssh root@192.168.8.1 'opkg update && opkg install \
  /tmp/wattline-bt_*.ipk \
  /tmp/wattlined_*.ipk \
  /tmp/luci-app-wattline_*.ipk'
```

With feed access, opkg pulls the bluez stack automatically; installs clean
with no `--force` flags (verified on a Spitz AX). `wattlined`'s `postinst`
runs `/etc/uci-defaults/99-wattline` (generates a random API token into
`/etc/config/wattline` if unset), then enables and **restart**s the procd
service (restart, so an upgrade picks up the new binary and token).

### Install from the hosted opkg feed (recommended)

A ready-to-use opkg feed is published via GitHub Pages at
**https://keithah.github.io/openwrt-wattline/**. Register it once and opkg
handles install and upgrades (it pulls the `bluez`/`dbus`/`kmod-bluetooth`
dependencies by name from the router's own feeds):

```sh
ssh root@192.168.8.1
echo 'src/gz wattline https://keithah.github.io/openwrt-wattline' >> /etc/opkg/customfeeds.conf
opkg update
opkg install wattlined luci-app-wattline gl-app-wattline   # first time
opkg upgrade wattlined luci-app-wattline gl-app-wattline    # later releases
```

The feed is regenerated from `package/out/` on each release
(`make -C package feed`, then published to the `gh-pages` branch).

### Install from a release

Prefer the hosted feed above. If you'd rather grab files directly, each
[GitHub release](https://github.com/keithah/openwrt-wattline/releases) attaches
the four `.ipk`s plus a ready-made opkg feed index (`Packages`, `Packages.gz`).
To install without building anything:

```sh
# Download the ipks from the latest release straight to the router
ssh root@192.168.8.1 'cd /tmp && for p in \
    wattline-bt_VER_all.ipk \
    wattlined_VER_aarch64_cortex-a53.ipk \
    luci-app-wattline_VER_all.ipk \
    gl-app-wattline_VER_all.ipk; do
  wget -q "https://github.com/keithah/openwrt-wattline/releases/latest/download/$p"
done
opkg update && opkg install /tmp/wattline-bt_*.ipk /tmp/wattlined_*.ipk \
  /tmp/luci-app-wattline_*.ipk /tmp/gl-app-wattline_*.ipk'
```

Replace `VER` with the release version (e.g. `1.2.2`). The release assets are
flat files, so they can also be registered directly as an opkg feed:

```sh
echo 'src/gz wattline https://github.com/keithah/openwrt-wattline/releases/latest/download' \
  >> /etc/opkg/customfeeds.conf
opkg update && opkg install wattlined luci-app-wattline gl-app-wattline
```

### Updating without a manual reinstall (opkg feed)

Copying ipks by hand is only for first-time/dev installs. For updates, host
the packages as an **opkg feed** (an HTTP dir with a `Packages.gz` index) —
the same mechanism GL's own apps and the unofficial Speedify use — then the
router upgrades with `opkg upgrade` (or the GL **Plug-ins** page).

```sh
# Build all ipks + the feed index. BUMP THE VERSION each release so opkg
# detects an upgrade (the Version: field, filename, and index must all match —
# the Makefile injects VERSION into all three).
make -C package VERSION=1.2.2 feed
# → package/out/{*.ipk, Packages, Packages.gz}
```

Host `package/out/` somewhere the router can reach (GitHub Pages/Releases, a
VPS, etc.), then register it once on the router:

```sh
echo 'src/gz wattline https://your-host/wattline-feed' >> /etc/opkg/customfeeds.conf
opkg update
opkg install wattlined luci-app-wattline   # first time (pulls deps by name)
opkg upgrade wattlined luci-app-wattline    # thereafter — no reinstall
```

Verified on a GL-X3000: bumping to 1.0.1 and `opkg upgrade` moved the install
from 1.0.0 → 1.0.1 with no `--force` flag, and `postinst` restarted the daemon
onto the new binary. (Note: `opkg install` of a *same-version* local ipk is a
no-op — use `--force-reinstall` when iterating on an unchanged version, or just
bump `VERSION`.)

### USB BLE dongle requirement

Most GL.iNet routers (including the Spitz AX / GL-X3000) have no onboard
Bluetooth radio — you need a USB BLE dongle. `wattline-bt` pulls the driver
stack (`kmod-bluetooth` ships `btusb.ko`). **Use a generic CSR8510-class
dongle:** the GL-X3000 kernel builds `btusb` but has the RTL/BCM/MTK btusb
sub-drivers disabled (`CONFIG_BT_HCIBTUSB_RTL` etc. unset), so an RTL8761B
dongle likely won't work. After plugging in, confirm the adapter with
`hciconfig` / `bluetoothctl list`; the daemon logs `adapter /org/bluez/hci0
does not exist` and retries until one appears.

## Config keys (`/etc/config/wattline`)

```
config wattline 'main'
	option device_mac ''      # BLE MAC of the Link-Power; blank = scan by name
	option pin '020555'       # BLE pairing PIN
	option port '8377'        # HTTP API port
	option lan_api '1'        # 1 = bind 0.0.0.0 (LAN-reachable); 0 = 127.0.0.1 only
	option token ''           # bearer token; auto-generated on first install if blank

config rule 'no_input_shutdown'
	option enabled '0'
	option condition 'input_power'
	option state 'absent'
	option hold '10m'
	list action 'webhook:https://ntfy.sh/CHANGME?msg=input+lost'
	list action 'shutdown'
	option confirm_shutdown '1'
```

Rules can also be managed live through the API (see below); edits made via
the API are persisted back to this UCI file, and the running daemon is
reloaded with `SIGHUP` (procd's `service_triggers`/`reload_service` wire this
up automatically on `uci commit wattline` + `ubus call service reload`, or
manually via `/etc/init.d/wattlined reload`).

## API

All endpoints require `Authorization: Bearer <token>`, where `<token>` is
`uci get wattline.main.token` (auto-generated on install; rotate it by
setting `option token` and running `uci commit wattline` + a service
restart).

```sh
TOKEN=$(ssh root@192.168.8.1 uci get wattline.main.token)

# Live telemetry (battery %, watts in/out, port states, connection status)
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/telemetry

# Daemon/BLE session status
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/status

# Recent telemetry history / rule-fire event log
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/history
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/events

# Rules: list, create, update, delete
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/rules
curl -s -H "Authorization: Bearer $TOKEN" -X POST -d @rule.json \
	http://192.168.8.1:8377/api/v1/rules
curl -s -H "Authorization: Bearer $TOKEN" -X PUT -d @rule.json \
	http://192.168.8.1:8377/api/v1/rules/no_input_shutdown
curl -s -H "Authorization: Bearer $TOKEN" -X DELETE \
	http://192.168.8.1:8377/api/v1/rules/no_input_shutdown

# Manual device control (DC output, Type-C output, bypass, restart, shutdown)
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"action":"shutdown"}' http://192.168.8.1:8377/api/v1/device/action

# Device settings (require a live connection; 503 when disconnected)
# USB-C output power limit — read all types, set output cap, or clear to default
curl -s -H "Authorization: Bearer $TOKEN" \
	http://192.168.8.1:8377/api/v1/device/usbc-limit
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"type":"output","watts":100}' http://192.168.8.1:8377/api/v1/device/usbc-limit
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"type":"output","clear":true}' http://192.168.8.1:8377/api/v1/device/usbc-limit
# DC bypass engage-voltage threshold
curl -s -H "Authorization: Bearer $TOKEN" \
	http://192.168.8.1:8377/api/v1/device/bypass-threshold
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"volts":19.6}' http://192.168.8.1:8377/api/v1/device/bypass-threshold
# On-device schedules (timers that fire even when the router/BLE is down)
curl -s -H "Authorization: Bearer $TOKEN" \
	http://192.168.8.1:8377/api/v1/device/schedules
# add: type 0=once,1=daily,2=weekly,3=monthly; action 0=off,1=on (omit id to add)
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"type":1,"hour":22,"minute":30,"action":0}' \
	http://192.168.8.1:8377/api/v1/device/schedules
curl -s -H "Authorization: Bearer $TOKEN" -X DELETE \
	http://192.168.8.1:8377/api/v1/device/schedules/0

# Pairing (first-run setup; also driven by the GL panel / LuCI UI)
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	http://192.168.8.1:8377/api/v1/pairing/scan            # async, ~12 s
curl -s -H "Authorization: Bearer $TOKEN" \
	http://192.168.8.1:8377/api/v1/pairing/status          # stage + devices found
curl -s -H "Authorization: Bearer $TOKEN" -X POST \
	-d '{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}' \
	http://192.168.8.1:8377/api/v1/pairing/pair            # async pair + trust
curl -s -H "Authorization: Bearer $TOKEN" -X DELETE \
	http://192.168.8.1:8377/api/v1/pairing/device/DC:04:5A:EB:72:2B
```

Pairing runs asynchronously: poll `/pairing/status` for
`idle|scanning|pairing|paired|error`. While a scan or pair is in flight the
daemon pauses its auto-reconnect loop (the Link-Power accepts one BLE central
at a time). On success the MAC and PIN are persisted to `/etc/config/wattline`
and the connector reconnects; treat `/api/v1/status` reporting
`"connected": true` as the real success signal (see
[`docs/pairing-design.md`](docs/pairing-design.md)).

If `lan_api` is `1`, these are reachable from any LAN host at the router's
address; set it to `0` to restrict the API to `localhost` on the router.

## Web UI

Two UIs ship, both thin clients over the API above:

- **GL.iNet admin panel** (`gl-app-wattline`) — a native entry under
  **Applications → Wattline** in the GL UI (`http://<router>`), matching how
  Speedify/Tailscale integrate. Loads with the panel session (no separate
  login, no iframe): the view calls an oui-httpd RPC for the API token, then
  polls the daemon directly. GL's oui frontend is closed-source, so this was
  reverse-engineered — the full mechanism is in
  [`docs/gl-panel-integration.md`](docs/gl-panel-integration.md).
- **LuCI** (`luci-app-wattline`) — open LuCI (System → Advanced Settings, or
  `http://<router>/cgi-bin/luci/`) → **Services → Wattline** for a live Status
  page, Rules editor, and Settings.

## Local/macOS development

The BLE transport (`internal/ble/tinygo.go`, using `tinygo.org/x/bluetooth`
over BlueZ/D-Bus) and the BlueZ pairing agent (`internal/ble/agent.go`) are
built only under `//go:build linux` — this is the production path on
OpenWrt. On non-Linux hosts, `internal/ble/unsupported.go` is compiled in
instead and returns an explicit "BLE transport is Linux/BlueZ only" error
from `ScanAndConnect`, so `go run ./cmd/wattlined` on macOS today will start
the HTTP API and rules engine but will not connect to a real Link-Power over
CoreBluetooth.

Everything above the transport boundary (config/UCI parsing, the rules
engine, the HTTP API, and device actions) is plain Go and fully testable on
macOS:

```sh
cd router
go build ./...
go test ./...
go run ./cmd/wattlined -config ./testdata/wattline   # API + rules only, no BLE
```

To exercise real device I/O against a Link-Power from a macOS dev machine,
either:

- run `go run ./cmd/wattlined` on a Linux host/VM/container with a BLE
  adapter and BlueZ (`bluetoothd` running, adapter powered), or
- extend `internal/ble/tinygo.go`'s build tag to include `darwin` — the
  underlying `tinygo.org/x/bluetooth` library does support a CoreBluetooth
  backend on macOS — and validate `RegisterPairingAgent`'s BlueZ-specific
  D-Bus calls are gated appropriately for that platform before relying on it
  for anything beyond scan/connect.

## Verifying on hardware (Spitz AX)

See `package/` install steps above, then:

```sh
ssh root@192.168.8.1 'logread -e wattline | tail'
```

Expect log lines for adapter enable, scan, connect, and identity handshake.
Enable the `no_input_shutdown` rule (with a real webhook URL), unplug input
power, and confirm the webhook fires and the unit shuts down after the
configured `hold` (10m default); confirm plugging PD input back in wakes the
unit.
