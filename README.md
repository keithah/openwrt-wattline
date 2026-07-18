# openwrt-wattline

`wattlined` connects an OpenWrt router to a PeakDo Link-Power portable power
station over Bluetooth LE. It exposes local HTTP and HTTPS APIs, keeps telemetry,
runs rules and webhooks, publishes LAN discovery, and ships both LuCI and native
GL.iNet administration panels. It sends no telemetry off-box and has no cloud
dependency.

The project targets the GL.iNet Spitz AX (GL-X3000) and other
`aarch64_cortex-a53` OpenWrt routers. Most routers require a USB Bluetooth
adapter. CSR8510 adapters work with the stock driver; RTL8761B adapters need the
kernel-module and firmware work described in [`dongle-rtl8761b/`](dongle-rtl8761b/).

The authoritative client contract is [`docs/api.md`](docs/api.md). The separate
[`docs/API.md`](docs/API.md) is the read-only Link-Power BLE protocol reference;
it is not a REST API.

## Build the packages

The host needs Go, gzip, and GNU tar (`gtar` on macOS):

```sh
make -C package clean all
package/check-ipk-metadata.sh package/out/*.ipk
```

The default version is `1.3.0`. Override it consistently with, for example,
`make -C package VERSION=1.3.1 all`. A build produces:

- `wattlined_VERSION_aarch64_cortex-a53.ipk`: daemon, procd service,
  first-boot initialization, firewall reconciliation, and interface hotplug;
- `wattline-bt_VERSION_all.ipk`: BlueZ and kernel Bluetooth dependencies;
- `luci-app-wattline_VERSION_all.ipk`: LuCI panel; and
- `gl-app-wattline_VERSION_all.ipk`: native GL.iNet panel.

These OpenWrt packages are gzip-compressed outer tar archives containing
`debian-binary`, `control.tar.gz`, and `data.tar.gz`. Both the outer and inner
archives use ustar. They are deliberately not ar/deb archives: the GL-X3000
opkg version rejects pax headers and has crashed on ar-form packages.

## Install or upgrade a router

Dropbear may not provide an scp server, so stream the files over SSH:

```sh
for f in package/out/*.ipk; do
  ssh root@192.168.8.1 "cat > /tmp/$(basename "$f")" < "$f"
done

ssh root@192.168.8.1 'opkg update && opkg install \
  /tmp/wattline-bt_1.3.0_all.ipk \
  /tmp/wattlined_1.3.0_aarch64_cortex-a53.ipk \
  /tmp/luci-app-wattline_1.3.0_all.ipk \
  /tmp/gl-app-wattline_1.3.0_all.ipk'
```

For a rebuilt package with the same version, use
`opkg install --force-reinstall /tmp/PACKAGE.ipk`; otherwise opkg skips it.
Release builds should bump `VERSION`. `make -C package VERSION=1.3.1 feed`
also creates `Packages` and `Packages.gz` for a normal opkg feed.

Installation creates an admin bootstrap token, a managed-token store, and an
ECDSA P-256 self-signed certificate, then enables and restarts the daemon.
Private credential directories and files are mode `0700`/`0600`. Confirm startup:

```sh
ssh root@192.168.8.1 '/etc/init.d/wattlined status; logread -e wattline | tail -n 50'
```

## Configuration

The main UCI section is `/etc/config/wattline`:

```text
config wattline 'main'
	option device_mac ''
	option pin '020555'
	option token ''
	option http_enabled '1'
	option http_addr4 '0.0.0.0'
	option http_addr6 '::'
	option port '8377'
	option https_enabled '1'
	option https_addr4 '0.0.0.0'
	option https_addr6 '::'
	option https_port '8378'
	option tls_cert '/etc/wattline/tls/server.crt'
	option tls_key '/etc/wattline/tls/server.key'
	option token_store '/etc/wattline/tokens.json'
	option pairing_ttl '5m'
	option pairing_always_on '0'
	option advanced '0'
	option mdns_enabled '1'
	list mdns_interface 'br-lan'
	option wan_access '0'
```

`pin` is the six-digit Link-Power BLE PIN (returned as `ble_pin` by the settings
API). `token` is the
non-revocable bootstrap administrator secret. The first-boot initializer fills
blank credentials and missing modern keys without replacing existing values.
Legacy `port` and `lan_api` continue to load; new installations should use the
listener keys above.

HTTP and HTTPS bind IPv4 and IPv6 independently. Both default to all addresses,
which makes LAN, `tailscale0`, and other WireGuard/VPN interfaces reachable.
That does not open the OpenWrt WAN firewall. `wan_access=0` is the safe default.
Setting it to `1` installs TCP WAN rules for enabled listeners and logs
`insecure — use TLS/VPN`; direct plain-HTTP WAN exposure is unsafe.

Apply UCI changes with:

```sh
uci commit wattline
/etc/init.d/wattlined reload
```

Rule-only changes use SIGHUP and retain the BLE link. A changed main section is
restarted so listeners, authentication, discovery, and firewall policy cannot
drift. The settings API reports `restart_required` for changes that need this.

## Connect and enroll clients

Read the bootstrap token on the router and test both transports:

```sh
TOKEN=$(ssh root@192.168.8.1 'uci -q get wattline.main.token')
HOST=$(ssh root@192.168.8.1 'hostname')
ssh root@192.168.8.1 'cat /etc/wattline/tls/server.crt' > wattline-server.crt
curl -H "Authorization: Bearer $TOKEN" \
  http://192.168.8.1:8377/api/v1/device
curl --cacert wattline-server.crt --resolve "$HOST:8378:192.168.8.1" \
  -H "Authorization: Bearer $TOKEN" "https://$HOST:8378/api/v1/device"
```

The curl example explicitly trusts the copied certificate and uses its hostname.
Apps follow the stricter DER SHA-256 pinning flow in the API contract: verify the
pin before sending a bearer token, hard-fail a mismatch, and never silently
downgrade to HTTP.

For app enrollment, an administrator opens pairing mode and displays its PIN or
QR. Each client exchanges the short-lived PIN for its own token:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://192.168.8.1:8377/api/v1/pairing-mode
curl -H "Authorization: Bearer $TOKEN" \
  http://192.168.8.1:8377/api/v1/pairing-mode/qr.png > wattline-pair.png
curl -H 'Content-Type: application/json' \
  -d '{"pin":"123456","label":"Keith iPhone"}' \
  http://192.168.8.1:8377/api/v1/pair
```

The pair response returns a new `wlt_...` client token only once, the HTTP/HTTPS
base URLs, MagicDNS name when available, and the DER-certificate SHA-256 for
pinning. List metadata or revoke a managed token with admin authentication:

```sh
curl -H "Authorization: Bearer $TOKEN" http://192.168.8.1:8377/api/v1/tokens
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  http://192.168.8.1:8377/api/v1/tokens/TOKEN_ID
```

`pairing_always_on=1` removes the explicit UI-button requirement but leaves an
enrollment PIN continuously available and rotating. Use it only when that local
attack-surface tradeoff is acceptable. It does not expose the BLE PIN.

## Discovery and VPN reachability

After the BLE identity is known, the daemon advertises `_wattline._tcp` on the
configured LAN interfaces. Its TXT record includes API/version, device MAC,
model, CID, features, authentication mode, and TLS fingerprint. It never
advertises on WAN or tunnel interfaces. Browse it from a LAN host with:

```sh
dns-sd -B _wattline._tcp local
avahi-browse -rt _wattline._tcp
```

mDNS is LAN-only. Remote clients save their issued token and use the router's
Tailscale MagicDNS name or another VPN address. The mDNS `id` correlates the LAN
advertisement with the same remote device. Binding all interfaces is sufficient
for Tailscale and ordinary WireGuard interfaces when their own ACL/firewall
policy permits the connection. `tailscale serve` is optional, not required.

## BLE-device pairing and operation

The LuCI and GL panels can scan, pair, trust, and remove a Link-Power. Pairing is
asynchronous: poll `GET /api/v1/pairing/status` until `paired` or `error`, then
use `GET /api/v1/device` as the authoritative connection result. The default BLE
PIN is `020555`; it is distinct from the short-lived API-client pairing PIN.

Both panels also expose identity, controls, pairing PIN/QR, token revocation,
listener/TLS/reachability policy, and advanced settings. Rules, webhooks,
telemetry history, and SSE remain available through the versioned API.

The API exposes OTA mode INFO plus enter and exit operations. It does **not**
download, erase, program, verify, upload, or flash firmware.

## Development and verification

Transport-independent code runs on any Go development host:

```sh
go test -count=1 ./...
go test -race -count=1 ./internal/state/ ./internal/ble/ ./internal/control/ \
  ./internal/auth/ ./internal/api/ ./internal/server/ ./internal/discovery/
go vet ./...
```

Production BLE uses Linux/BlueZ. Non-Linux builds use the explicit unsupported
transport, so a macOS daemon can exercise API/rules behavior but cannot validate
real Link-Power traffic. Follow the
[`GL-X3000 verification checklist`](docs/gl-x3000-verification.md) for the
remaining on-target and real-BLE checks. No item in that checklist is considered
verified merely because unit tests pass.
