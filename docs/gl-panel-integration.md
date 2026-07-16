# GL.iNet admin-panel ("Applications") integration — recipe & status

**Status: IMPLEMENTED** as `gl-app-wattline` (package/gl-app-wattline/), verified on
a live GL-X3000. It adds a native **Applications → Wattline** entry that loads with
no LuCI login and no iframe. This note captures the reverse-engineered mechanism.
The LuCI app also still ships as an alternative UI (System → Advanced → LuCI →
Services → Wattline).

## As-built summary

- **View bundle** `www/views/gl-sdk4-ui-wattline.common.js` — oui loads a view via
  `const component = eval(res.data)`, so the file must *evaluate to* a Vue 2
  component (a returning IIFE, not `module.exports`). Uses `window.Vue` render
  functions; renders the battery ring + port cards.
- **No second login:** the view POSTs `/rpc` (JSON-RPC `call`, `params:[sid, "wattline",
  method, args]`, `sid` = the `Admin-Token` cookie = the panel session) to the Lua
  handler, then polls the daemon REST API directly (CORS is enabled on the daemon).
- **Lua RPC handler** `usr/lib/oui-httpd/rpc/wattline` returns `{get_config=…}` reading
  the API token from UCI (the nginx-lua sandbox blocks `io.popen`, so the daemon stays
  the single telemetry source). oui-httpd allows the `root` aclgroup (admin login) by
  default — no ACL file needed.
- **Menu** `usr/share/oui/menu.d/wattline.json` (`parent: "applications"`).
- Packaged as `gl-app-wattline` (arch `all`, Depends `wattlined`), installable/upgradable
  via the opkg feed.

Below is the original recipe / RE detail.

## How the GL panel works

The GL admin UI at `192.168.8.1` is **oui** (a Vue SPA GL forked from
[zhaojh329/oui](https://github.com/zhaojh329/oui)), served by nginx from `/www/`.
It is **not** LuCI. An app in the left-nav is three pieces:

1. **Menu manifest** — `/usr/share/oui/menu.d/<name>.json`. This decides placement.
   The **Applications** group is `parent: "applications"`. Live AdGuard example:
   ```json
   {
     "index": 80, "view": "adguardhome", "title": "AdGuard Home", "level": 2,
     "parent": "applications", "parent_icon": "application", "parent_index": 55,
     "show_mode": ["router"]
   }
   ```
   (Speedify uses `parent: "vpn"`; Tailscale/ZeroTier/AdGuard use `parent: "applications"`.)

2. **Compiled view bundle** — `/www/views/gl-sdk4-ui-<view>.common.js.gz`, where
   `<view>` matches the manifest's `view`. This is a **minified webpack UMD module**
   (`module.exports=function(t){…}`) wrapping a compiled Vue single-file component.
   This is the hard dependency: it must be built with GL's oui frontend toolchain
   (Node/webpack/Vue). There is **no** declarative `iframe`/`url`/`link`/`redirect`
   menu type — every GL app, including AdGuard (which has its own web UI), ships a
   native compiled Vue wrapper. Confirmed by grepping every `menu.d/*.json`.

3. **Backend RPC** — a Lua handler at `/usr/lib/oui-httpd/rpc/<name>` plus an nginx
   fragment in `/etc/nginx/gl-conf.d/<name>.conf`. Called via oui's JSON-RPC.
   For Wattline this shim can simply proxy the daemon's REST API on `:8377`
   (or read UCI + shell out), so no daemon changes are needed.

## Precedent

`luci-app-speedify` in `/www` is the community **speedifyunofficial** project — a
third party that integrated into the GL UI. It ships a compiled webpack bundle
(`chunk-*.js.gz`) + nginx fragments, confirming third-party GL-UI apps are
possible but require the compiled-bundle approach.

## View-module contract (reverse-engineered from live bundles)

GL's oui **frontend source is not public** (github.com/gl-inet publishes the SDK,
ipks, and docs — not the Vue app). So there's no "stand up GL's build"; a native
view must reproduce the bundle format by RE. Decompressing
`/www/views/gl-sdk4-ui-igmp.common.js.gz` (and the AdGuard RPC handler) shows:

- **Bundle**: a minified webpack UMD — `module.exports=function(webpackBootstrap){…}({…modules…}).default`. Net effect: **`module.exports` IS the Vue component** (a compiled SFC options object).
- **Vue 2**, provided as an external global `window.Vue` (render fns use `_c`/`staticRenderFns`, `t._v`, `t.$set`, `this.$t`).
- **Global components** referenced by tag, resolved at runtime by the oui app (NOT bundled): `gl-card`, `gl-switch`, `gl-button`, `gl-title`, plus Element UI `el-select`/`el-option`.
- **Backend access** via a **Vuex `$store`** (`this.$store` + an rpc `.call`), plus `this.$message` (Element UI), `this.$t` (vue-i18n), `this.$deepCopy` (GL helper) — all only available inside the oui runtime.
- **RPC handler**: `/usr/lib/oui-httpd/rpc/<name>` is **Lua** (shipped compiled, but source .lua should work) using `require "oui.rpc"`, `oui.ubus`, `oui.uci`, `oui.fs`; it declares methods with a `params` table and an `access` ACL. oui-httpd exposes these as JSON-RPC that the `$store` calls.

**Feasibility:** the backend (Lua RPC over the daemon's REST API), the `menu.d`
entry, packaging, and feed are all straightforward. The **frontend bundle is the
hard/fragile part** — it must reproduce GL's closed webpack/Vue2/Vuex output well
enough for oui-ui-core to load and mount it, and any native UI that shows live data
must use the `$store` rpc contract (not fully pinned from a single bundle).

**Auth boundary caveat for any iframe shortcut:** a minimal view could just render
an `<iframe>`, but the GL panel, LuCI, and the daemon API each have separate
auth/session, so an embedded LuCI page would hit LuCI's own login, and a static
page can't read the daemon token. A clean native view therefore wants the real
Vuex-rpc path, not an iframe.

## v1.x implementation plan (when picked up)

1. Stand up GL's oui frontend build from [glinet4.x](https://github.com/gl-inet/glinet4.x)
   / [sdk](https://github.com/gl-inet/sdk); confirm the exact oui/webpack version
   matching the target firmware.
2. Author `views/wattline/` as a Vue view (battery hero, port toggles, rules grid,
   live telemetry) calling an oui-httpd RPC.
3. Write `/usr/lib/oui-httpd/rpc/wattline` (Lua) that proxies the daemon REST API
   (`GET /telemetry`, `GET/POST /rules`, `POST /device/action`) using the UCI token.
4. Package: the built `gl-sdk4-ui-wattline.common.js.gz` + `menu.d/wattline.json`
   (`parent: applications`) + the RPC + nginx fragment, as a gzip-tar/ustar ipk
   (see the Makefile's format notes — GL's opkg segfaults on ar-format ipks and
   rejects pax tar headers).
5. Reuse the daemon and its REST API unchanged; this is purely an additional UI.

## Gotchas already learned (all in the packaging Makefile)

- `.ipk` must be a **gzipped tar** (not ar/.deb) or opkg segfaults.
- Tar members must be **ustar** format (`--format ustar`); opkg rejects pax headers.
- LuCI/oui static assets must land under `/www/…`, not `/htdocs/…`.
