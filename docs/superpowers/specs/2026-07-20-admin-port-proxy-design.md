# Wattline GL Admin-Port Proxy Design

**Date:** 2026-07-20  
**Status:** Approved design, pending implementation  
**Scope:** `gl-app-wattline` packaging only

## Goal

Expose Wattline's existing versioned HTTP API through the GL.iNet SDK4 nginx listener on port 80 at a stable `/wattline/` prefix. This gives the GL admin panel and, subject to live relay verification, GoodCloud Remote Web Access a same-origin route to Wattline without changing or removing the existing API listener on port 8377.

The concrete mappings are:

- `/wattline/status` -> `http://127.0.0.1:8377/api/v1/status`
- `/wattline/events` -> `http://127.0.0.1:8377/api/v1/events`
- every other `/wattline/<path>` -> `http://127.0.0.1:8377/api/v1/<path>`

## Non-goals

- Do not change `wattlined`, its bind address, port, REST routes, BLE behavior, authentication, CORS policy, or SSE implementation.
- Do not remove or redirect the direct port-8377 API.
- Do not add a generic nginx integration to the base `wattlined` package. The proxy is specific to GL.iNet's SDK4 admin stack.
- Do not weaken Wattline authentication to accommodate GoodCloud.
- Do not make Swift-app changes.

## Hardware Evidence and Platform Convention

A read-only inspection of a live GL-E5800 running GL.iNet firmware 4.8.5 confirmed the SDK4 convention that nginx includes `/etc/nginx/gl-conf.d/*.conf`. AdGuard installs a dedicated `/control/` proxy location. Speedify installs an app-specific location file and links it into `gl-conf.d`; its WebSocket location explicitly uses HTTP/1.1 and application-owned authentication.

The router's generic `location /` invokes `/usr/share/gl-ngx/oui-access.lua`, but more-specific application locations do not inherit that directive. The Lua file primarily handles host, redirect, and initialization checks; it is not a stable reusable admin-session authorization interface. Consequently, Wattline must continue to authenticate its own route.

The target GL-X3000 at `192.168.8.1` and a browser-authenticated GoodCloud relay remain required for final platform verification.

## Nginx Route

`gl-app-wattline` will ship `/etc/nginx/conf.d/gl-app-wattline.locations` and manage this symlink using its package lifecycle scripts:

```text
/etc/nginx/gl-conf.d/gl-app-wattline.conf
  -> /etc/nginx/conf.d/gl-app-wattline.locations
```

This follows the Speedify packaging shape and lets `prerm` remove the active include before nginx is reloaded, while the package manager remains responsible for deleting the packaged source file.

The fragment will contain:

```nginx
location = /wattline {
    return 308 /wattline/;
}

location ^~ /wattline/ {
    proxy_pass http://127.0.0.1:8377/api/v1/;
    proxy_http_version 1.1;
    proxy_set_header Authorization $http_authorization;
    proxy_set_header Connection "";
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 1h;
}
```

The trailing slashes on both the location and `proxy_pass` are intentional: nginx replaces `/wattline/` with `/api/v1/`, preserving the remainder of the path and query string. The exact `/wattline` location provides a stable canonical redirect.

SSE requires HTTP/1.1, disabled proxy buffering and caching, an empty upstream `Connection` header, and a read timeout longer than normal idle intervals. Response headers, status codes, request bodies, query strings, and streaming bytes otherwise pass through unchanged.

## Authentication Decision

The proxied route requires Wattline's existing bearer token exactly as port 8377 does. Nginx explicitly forwards the client's `Authorization` header and never reads a token from UCI, injects a token, or removes authentication.

The resulting security boundaries are:

- **LAN port 80:** Wattline bearer authentication is required.
- **Direct port 8377:** existing Wattline bearer authentication is unchanged.
- **GoodCloud remote access:** the GoodCloud login/session is an outer access boundary and Wattline's bearer token remains the inner application boundary. A successful remote request must satisfy both.

This is preferable to coupling Wattline to an undocumented `oui` cookie or Lua implementation. It also prevents an authenticated router-admin browser session from silently granting Wattline API access to unrelated page content. The GL panel's existing authenticated RPC can continue to obtain the configured token for its own API calls.

GL.iNet documents that GoodCloud Remote Web Access opens the router web admin panel, but whether its relay forwards an arbitrary application subpath and preserves `Authorization` must be verified with a live, browser-authenticated GoodCloud session. If the relay strips the header, that is a compatibility limitation to solve without dropping Wattline authentication.

## Package Ownership and Lifecycle

Only `gl-app-wattline` owns the nginx integration because that package already targets the native GL SDK4 `oui` panel. The generic `wattlined` and LuCI packages remain portable to non-GL OpenWrt installations.

The package build will:

1. Stage the location source at `/etc/nginx/conf.d/gl-app-wattline.locations`.
2. Install an executable `postinst` that creates or refreshes the `gl-conf.d` symlink, validates nginx configuration, and reloads nginx when running on a live root.
3. Install an executable `prerm` that removes only Wattline's managed symlink, validates the remaining nginx configuration, and reloads nginx when running on a live root.
4. Skip live-service actions when `IPKG_INSTROOT` is set for image construction.

Scripts must be idempotent. They must not overwrite an unrelated regular file at the managed symlink path. A failed `nginx -t` must not reload nginx. Installation must restore the previous link state if enabling the Wattline fragment makes validation fail, preventing a bad fragment from disrupting the admin panel. Removal must leave nginx usable even if the link is already absent.

No new runtime dependency is needed: `gl-app-wattline` is specific to GL SDK4, where nginx and the `gl-conf.d` convention are platform facilities. The existing gzipped-ustar `.ipk` format and normalized archive metadata remain unchanged.

## Documentation

Implementation will add `docs/admin-port-proxy.md` as the operator-facing record of:

- both API base URLs;
- path rewriting;
- bearer-token behavior on LAN and GoodCloud;
- SSE proxy requirements;
- install/remove ownership;
- the unverified GoodCloud relay behavior and its explicit verification steps.

The router API contract will also note `http://<router>/wattline/` as an additional GL-package base URL without replacing `http://<router>:8377/api/v1/`.

## Test Strategy

Implementation is test-driven. Package-level tests will cover:

- the exact `/wattline/` to `/api/v1/` proxy mapping;
- explicit Authorization forwarding;
- the SSE directives (`proxy_http_version 1.1`, empty `Connection`, buffering/cache disabled, long timeout);
- staging the fragment and both lifecycle scripts into the `.ipk`;
- executable modes for lifecycle scripts;
- idempotent install and removal using a fake nginx/init environment;
- refusal to overwrite an unrelated active include;
- validation failure rollback and no reload on invalid configuration;
- preservation of the repository's gzip-wrapped ustar package format.

Local verification will run the package test suite, `go test ./...`, and `make -C package all` plus package metadata checks.

## Live GL-X3000 Verification

Before claiming hardware completion, perform these checks on `192.168.8.1`:

1. Record current package, nginx, Wattline, and direct-port health; preserve a recovery path.
2. Install only the rebuilt `gl-app-wattline` package and confirm `nginx -t` succeeds.
3. Confirm an authenticated `GET http://192.168.8.1/wattline/status` returns the same payload and status as `GET http://192.168.8.1:8377/api/v1/status`.
4. Confirm omitting or corrupting the bearer token returns Wattline's normal `401` through both routes.
5. Open `/wattline/events`, confirm `Content-Type: text/event-stream`, observe an event promptly, and verify the connection remains streaming rather than buffered or closed.
6. Confirm direct port 8377 and existing BLE/UI behavior remain unaffected.
7. Remove `gl-app-wattline`; confirm its symlink is gone, `nginx -t` passes, `/wattline/status` is no longer proxied, and direct port 8377 still works.
8. Reinstall the package and repeat the health checks.

GoodCloud verification requires its authenticated remote-admin URL and cannot be inferred solely from LAN nginx behavior:

1. Without a GoodCloud session, confirm the relay denies access or requests login.
2. With a GoodCloud session and a valid Wattline bearer, request `/wattline/status` and open `/wattline/events`.
3. With a GoodCloud session but no Wattline bearer, confirm Wattline returns `401`.
4. Confirm the relay preserves `Authorization` and does not buffer SSE.

Until those remote checks pass, LAN admin-port support may be called verified, but GoodCloud compatibility must remain explicitly marked unverified.

