# Changelog

All notable changes to openwrt-wattline. Versions are the `.ipk` package
versions built from `package/Makefile`.

## Unreleased

- Added a dedicated **Power-loss shutdown** card to LuCI and the native GL
  panel. The reserved preset defaults to ten minutes of continuous input loss,
  resets when input returns or BLE disconnects, preserves compatible rule
  customizations, and requires confirmation before replacing a conflict.
- Documented that Link-Power shutdown also powers off the router and that
  recovery depends on Link-Power hardware wake, GL-X3000 boot, and daemon
  reconnect. “Last fired” means an attempted trigger, not confirmed shutdown.
- Host-side behavior, contract, and package checks cover the preset. Hardware
  verification remains NOT RUN pending a GL-X3000 and real Link-Power BLE.
- Hardened the full control API after final audit: OTA reconnect now scans both
  app and bootloader advertisement names, disconnected controls cannot reuse a
  stale session, write-only Current Time reads perform no endpoint I/O, OTA
  keeps the last stable app identity, and truncated command replies are rejected.
- Exposed all 15 decoded `FEATURES` bits; made rule mutation and router-originated
  manual webhooks admin-only; serialized rule persistence; capped request bodies
  and request-read time; and evicted slow SSE subscribers at a bounded queue.

## 1.2.3

- New **temperature** rule condition (USB-C port °C, op below/above) with
  hysteresis re-arm — e.g. disable USB-C output when the port runs hot.
  Editable in the LuCI rules UI.
- **CI/CD** via GitHub Actions: `go vet`/`go test`/package build on every push
  and PR, and a tag-driven release workflow that publishes the `.ipk`s, the
  GitHub release, and the `gh-pages` opkg feed.
- Investigated device-side charge management: the Link-Power exposes no
  supported "disable charge input" command (the `TYPEC_CONTROL` input selector
  is rejected), so battery-longevity control remains the existing DC **bypass**
  (pass-through without cycling the battery) plus its engage-voltage threshold.

## 1.2.2

- Fix the USB-C **temperature** reading rendering blank in the GL panel (the
  metric passed a Vue vnode where the others pass strings; Vue 2 needs children
  wrapped in an array).

## 1.2.1

- Escape option values when serializing `/etc/config/wattline`, so a quote or
  newline in an API-supplied value (e.g. a rule name) can no longer inject or
  corrupt UCI configuration.

## 1.2.0

New device controls, exposing protocol capabilities that were documented but
unreachable from the router. All validated end-to-end on live hardware.

- **Power off** and **Restart** buttons in the GL panel and LuCI (power off
  warns it is a hard shutdown — the device won't return over BLE until
  physically powered on).
- **USB-C output power limit** — set the cap to 30/45/60/65/100/140 W
  (`GET`/`POST /api/v1/device/usbc-limit`).
- **DC bypass threshold** — read/set the engage voltage
  (`GET`/`POST /api/v1/device/bypass-threshold`).
- **On-device schedules** — create/list/delete timers stored on the device that
  fire even when the router or BLE is down
  (`GET`/`POST`/`DELETE /api/v1/device/schedules`).

## 1.1.0

- **GUI pairing flow.** The daemon now performs authenticated BLE pairing
  (`org.bluez.Device1.Pair`) instead of only registering an agent: scan →
  select → PIN → pair → trust, driven from the GL panel and LuCI or the API
  (`/api/v1/pairing/*`). Pairing success is verified by a surviving protected
  handshake, not BlueZ's transient `Paired: yes`, before the device is saved.
- Connector gained pause/resume so pairing owns the single-central radio;
  failed handshakes now release the connection instead of leaking it.
- Runtime agent PIN override (rolled back on failure) and UCI persistence of
  the paired device.
- Packaging: builds require GNU tar; the Makefile and metadata checker resolve
  and share the same `gtar`.

## 1.0.0

- Initial release. `wattlined`: a static Go daemon speaking the PeakDo
  Link-Power BLE/GATT protocol, with an automation rules engine (input-power,
  battery-level, port-power, schedule conditions), a local HTTP API
  (telemetry, history, events, rules, manual control), and webhook actions.
- Two web UIs: a native GL.iNet admin-panel app and a LuCI app.
- Hand-rolled OpenWrt `.ipk` packaging (gzipped-ustar-tar form opkg accepts)
  plus an opkg feed index generator.
- Guide for making an RTL8761B USB BLE dongle work on the GL-X3000's 5.4
  kernel (`dongle-rtl8761b/`).
