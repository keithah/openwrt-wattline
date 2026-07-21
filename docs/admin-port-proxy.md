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
proxy buffering and caching, and permits a one-hour read so `/wattline/events`
remains an SSE stream.

## Package lifecycle

`gl-app-wattline` owns the source fragment and manages only its own
`gl-conf.d` symlink. Install and removal validate nginx before reload and roll
back the symlink change if validation fails. The generic `wattlined` package
does not own GL-specific nginx files.

## Verification status

- LAN GL-X3000 verification: pending
- GoodCloud relay verification: pending

GoodCloud must be tested through an authenticated remote-admin URL to prove
that it forwards `/wattline/`, preserves `Authorization`, and does not buffer
SSE. Until then, do not describe GoodCloud compatibility as verified.
