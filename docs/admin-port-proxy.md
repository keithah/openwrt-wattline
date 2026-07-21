# GL admin-port proxy

Installing `gl-app-wattline` adds `http://ROUTER/wattline/` as an additional
base for the versioned Wattline API. It does not replace
`http://ROUTER:8377/api/v1/` or change the daemon listeners.

| Admin-port request | Direct daemon request |
|---|---|
| `/wattline/status` | `/api/v1/status` |
| `/wattline/events` | `/api/v1/events` |
| `/wattline/<path>` | `/api/v1/<path>` |

## Authentication decision

Every protected request still sends:

```text
Authorization: Bearer TOKEN
```

The nginx proxy preserves that header; it never embeds the bootstrap token.
LAN access requires Wattline's bearer. GoodCloud remote access requires the
GoodCloud session plus the Wattline bearer token. This deliberately keeps
Wattline independent of undocumented `oui` cookies and Lua internals.

## Streaming

The package uses HTTP/1.1, clears the upstream `Connection` header, disables
proxy buffering, configures no proxy cache, and permits a one-hour read so
`/wattline/events` remains an SSE stream. GL firmware builds nginx with
`--without-http-cache`, so the fragment deliberately uses no cache-module
directives.

## Package lifecycle

`gl-app-wattline` owns the source fragment and manages only its own
`gl-conf.d` symlink. Install and removal validate nginx before reload and roll
back the symlink change if validation fails. The generic `wattlined` package
does not own GL-specific nginx files. GL's nginx init script has no reload
handler, so the lifecycle scripts use nginx's native `-s reload` signal after
successful validation.

## Verification status

- Local package verification (2026-07-21): passed. All Go tests and the ten
  package shell suites passed; all five gzip-ustar packages built and passed
  both metadata checks.
- Live GL SDK4 proxy verification (2026-07-21): passed on a GL-E5800 running
  firmware 4.8.5, OpenWrt 23.05.4, and nginx 1.26.1. A temporary
  localhost-only, no-Bluetooth API fixture was installed as `wattlined`; it
  verified normal `gl-app-wattline` install, same-version force-reinstall,
  removal, reinstall, and final removal. `/wattline/status` preserved the
  rewritten `/api/v1/status` path, query string, and Authorization header;
  missing authorization returned `401`. `/wattline/events` delivered its first
  SSE event within two seconds without delivering the fixture's delayed second
  event, both on-router and through the router's Tailscale address on port 80.
  Direct port 8377 stayed reachable while the proxy package was removed. The
  E5800 was restored with both fixture packages, all proxy/demo files, and the
  8377 listener absent; nginx remained valid and port 80 returned `200`.
- LAN GL-X3000 verification: pending. Strict non-interactive SSH to
  `192.168.8.1:22` timed out before the router identity and service baseline
  could be checked, so the E5800 result does not verify GL-X3000 firmware or a
  real Wattline/Bluetooth session.
- GoodCloud relay verification: pending

GoodCloud must be tested through an authenticated remote-admin URL to prove
that it forwards `/wattline/`, preserves `Authorization`, and does not buffer
SSE. Until then, do not describe GoodCloud compatibility as verified.
