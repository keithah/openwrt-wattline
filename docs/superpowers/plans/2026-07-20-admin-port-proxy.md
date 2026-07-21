# Wattline GL Admin-Port Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a GL.iNet SDK4 nginx route at `/wattline/` that proxies the unchanged Wattline v1 API on `127.0.0.1:8377`, including unbuffered SSE and existing bearer authentication.

**Architecture:** The GL-specific `gl-app-wattline` package ships one nginx location source under `/etc/nginx/conf.d` and activates it with a Speedify-style symlink in `/etc/nginx/gl-conf.d`. Idempotent package lifecycle scripts validate nginx before every reload and roll back a newly enabled or removed link if validation fails. The daemon and its direct listeners are untouched.

**Tech Stack:** POSIX shell, nginx reverse proxy, OpenWrt/GL.iNet `opkg` control scripts, gzip-wrapped ustar `.ipk`, Markdown documentation.

## Global Constraints

- Preserve `http://ROUTER:8377/api/v1` and all existing daemon, BLE, CORS, rule, webhook, and SSE behavior.
- Map `/wattline/<path>` exactly to `http://127.0.0.1:8377/api/v1/<path>`.
- Preserve and explicitly forward `Authorization`; never inject the UCI token into nginx.
- Use HTTP/1.1, an empty upstream `Connection` header, disabled buffering, no cache-module directive, and a one-hour read timeout for SSE. GL nginx is built with `--without-http-cache`.
- Package the integration only in `gl-app-wattline`.
- Keep package archives as gzip-wrapped ustar, never `ar`/Debian format or pax tar.
- Never reload nginx after a failed `nginx -t`.
- Do not overwrite any unrelated file or symlink in `/etc/nginx/gl-conf.d`.
- Treat GoodCloud support as unverified until tested through a browser-authenticated remote relay.

---

## File Structure

- Create `package/gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations`: the complete nginx routing and SSE contract.
- Modify `package/gl-app-wattline/CONTROL/postinst`: safely create the active include link, validate, roll back on failure, and reload.
- Create `package/gl-app-wattline/CONTROL/prerm`: safely remove only Wattline's include link, validate, restore on failure, and reload.
- Modify `package/Makefile`: stage the nginx source and mark both lifecycle scripts executable.
- Modify `package/check-ipk-metadata.sh`: assert the GL package contains the source fragment and executable lifecycle scripts with normalized ownership/modes.
- Create `package/tests/gl_nginx_proxy_test.sh`: static proxy-contract and executable lifecycle tests.
- Modify `.github/workflows/ci.yml`: run the new package test in CI.
- Modify `.github/workflows/release.yml`: run the new package test before release artifacts are built.
- Create `docs/admin-port-proxy.md`: operator-facing routing, authentication, lifecycle, and verification decision record.
- Modify `docs/api.md`: add the GL admin-port base URL to the authoritative HTTP contract.
- Modify `docs/gl-panel-integration.md`: replace the stale “future nginx fragment” wording with the as-built route and authentication model.

---

### Task 1: Package the Safe Nginx Proxy

**Files:**
- Create: `package/gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations`
- Modify: `package/gl-app-wattline/CONTROL/postinst`
- Create: `package/gl-app-wattline/CONTROL/prerm`
- Modify: `package/Makefile`
- Modify: `package/check-ipk-metadata.sh`
- Create: `package/tests/gl_nginx_proxy_test.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: nginx include directory `/etc/nginx/gl-conf.d`, packaged source `/etc/nginx/conf.d/gl-app-wattline.locations`, daemon base `http://127.0.0.1:8377/api/v1/`, `/etc/init.d/nginx reload`.
- Produces: managed symlink `/etc/nginx/gl-conf.d/gl-app-wattline.conf` and stable public prefix `/wattline/`.
- Test seams: `WATTLINE_NGINX_SOURCE`, `WATTLINE_NGINX_LINK`, `WATTLINE_NGINX_BIN`, and `WATTLINE_NGINX_INIT` override their production defaults only for an explicit process environment.

- [ ] **Step 1: Write the failing proxy and lifecycle test**

Create `package/tests/gl_nginx_proxy_test.sh` with a temporary fake nginx environment. The test must assert the literal route contract, then execute both control scripts through success, idempotence, conflict, and validation-failure cases:

```sh
#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
FRAGMENT="$ROOT/package/gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations"
POSTINST="$ROOT/package/gl-app-wattline/CONTROL/postinst"
PRERM="$ROOT/package/gl-app-wattline/CONTROL/prerm"
MAKEFILE="$ROOT/package/Makefile"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

need() {
	file=$1 pattern=$2 message=$3
	grep -Fq "$pattern" "$file" || { echo "missing: $message" >&2; exit 1; }
}

need "$FRAGMENT" 'location = /wattline {' 'canonical-prefix redirect'
need "$FRAGMENT" 'return 308 /wattline/;' 'canonical-prefix redirect target'
need "$FRAGMENT" 'location ^~ /wattline/ {' 'stable Wattline prefix'
need "$FRAGMENT" 'proxy_pass http://127.0.0.1:8377/api/v1/;' 'v1 upstream rewrite'
need "$FRAGMENT" 'proxy_http_version 1.1;' 'SSE HTTP version'
need "$FRAGMENT" 'proxy_set_header Authorization $http_authorization;' 'bearer forwarding'
need "$FRAGMENT" 'proxy_set_header Connection "";' 'persistent SSE upstream'
need "$FRAGMENT" 'proxy_buffering off;' 'SSE buffering disabled'
need "$FRAGMENT" 'proxy_read_timeout 1h;' 'long-lived SSE timeout'
if grep -Fq 'proxy_cache' "$FRAGMENT"; then
	echo 'forbidden: GL nginx is built --without-http-cache' >&2
	exit 1
fi
need "$MAKEFILE" 'gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations' 'fragment staging'
need "$MAKEFILE" 'stage-gl/CONTROL/prerm' 'prerm executable mode'

mkdir -p "$TMP/conf.d" "$TMP/gl-conf.d" "$TMP/bin"
cp "$FRAGMENT" "$TMP/conf.d/gl-app-wattline.locations"
CALLS="$TMP/calls"; export CALLS
cat > "$TMP/bin/nginx" <<'EOF'
#!/bin/sh
echo "nginx $*" >> "$CALLS"
[ "${FAIL_NGINX_TEST:-0}" != 1 ]
EOF
cat > "$TMP/bin/nginx-init" <<'EOF'
#!/bin/sh
echo "init $*" >> "$CALLS"
EOF
chmod +x "$TMP/bin/nginx" "$TMP/bin/nginx-init"

export WATTLINE_NGINX_SOURCE="$TMP/conf.d/gl-app-wattline.locations"
export WATTLINE_NGINX_LINK="$TMP/gl-conf.d/gl-app-wattline.conf"
export WATTLINE_NGINX_BIN="$TMP/bin/nginx"
export WATTLINE_NGINX_INIT="$TMP/bin/nginx-init"

sh "$POSTINST"
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$WATTLINE_NGINX_SOURCE" ]
grep -Fqx 'nginx -t' "$CALLS"
grep -Fqx 'init reload' "$CALLS"
sh "$POSTINST"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 2 ]

sh "$PRERM"
[ ! -e "$WATTLINE_NGINX_LINK" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 3 ]
sh "$PRERM"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq 3 ]

echo unrelated > "$WATTLINE_NGINX_LINK"
if sh "$POSTINST"; then echo 'postinst overwrote an unrelated file' >&2; exit 1; fi
grep -Fqx unrelated "$WATTLINE_NGINX_LINK"
rm "$WATTLINE_NGINX_LINK"
ln -s "$TMP/conf.d/other.locations" "$WATTLINE_NGINX_LINK"
if sh "$POSTINST"; then echo 'postinst overwrote an unrelated symlink' >&2; exit 1; fi
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$TMP/conf.d/other.locations" ]
rm "$WATTLINE_NGINX_LINK"

FAIL_NGINX_TEST=1; export FAIL_NGINX_TEST
before_reload=$(grep -Fc 'init reload' "$CALLS")
if sh "$POSTINST"; then echo 'postinst accepted invalid nginx config' >&2; exit 1; fi
[ ! -e "$WATTLINE_NGINX_LINK" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$before_reload" ]
unset FAIL_NGINX_TEST

sh "$POSTINST"
FAIL_NGINX_TEST=1; export FAIL_NGINX_TEST
if sh "$PRERM"; then echo 'prerm accepted invalid remaining nginx config' >&2; exit 1; fi
[ "$(readlink "$WATTLINE_NGINX_LINK")" = "$WATTLINE_NGINX_SOURCE" ]
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$((before_reload + 1))" ]
unset FAIL_NGINX_TEST

IPKG_INSTROOT="$TMP/root" sh "$POSTINST"
[ "$(grep -Fc 'init reload' "$CALLS")" -eq "$((before_reload + 1))" ]

echo 'GL nginx proxy tests passed'
```

- [ ] **Step 2: Run the new test and confirm the red state**

Run:

```bash
sh package/tests/gl_nginx_proxy_test.sh
```

Expected: FAIL because `gl-app-wattline.locations` and `CONTROL/prerm` do not exist.

- [ ] **Step 3: Add the exact nginx fragment**

Create `package/gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations`:

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
    proxy_read_timeout 1h;
}
```

- [ ] **Step 4: Implement safe activation and removal**

Replace `package/gl-app-wattline/CONTROL/postinst` with:

```sh
#!/bin/sh
set -u

[ -n "${IPKG_INSTROOT:-}" ] && exit 0

SOURCE=${WATTLINE_NGINX_SOURCE:-/etc/nginx/conf.d/gl-app-wattline.locations}
LINK=${WATTLINE_NGINX_LINK:-/etc/nginx/gl-conf.d/gl-app-wattline.conf}
NGINX=${WATTLINE_NGINX_BIN:-/usr/sbin/nginx}
NGINX_INIT=${WATTLINE_NGINX_INIT:-/etc/init.d/nginx}

if [ -L "$LINK" ]; then
	[ "$(readlink "$LINK")" = "$SOURCE" ] || {
		echo "gl-app-wattline: refusing to replace unrelated nginx include $LINK" >&2
		exit 1
	}
elif [ -e "$LINK" ]; then
	echo "gl-app-wattline: refusing to replace unrelated nginx include $LINK" >&2
	exit 1
else
	ln -s "$SOURCE" "$LINK" || exit 1
	created=1
fi

if ! "$NGINX" -t; then
	[ "${created:-0}" = 1 ] && rm -f "$LINK"
	echo 'gl-app-wattline: nginx validation failed; proxy was not enabled' >&2
	exit 1
fi

"$NGINX_INIT" reload
```

Create `package/gl-app-wattline/CONTROL/prerm`:

```sh
#!/bin/sh
set -u

[ -n "${IPKG_INSTROOT:-}" ] && exit 0

SOURCE=${WATTLINE_NGINX_SOURCE:-/etc/nginx/conf.d/gl-app-wattline.locations}
LINK=${WATTLINE_NGINX_LINK:-/etc/nginx/gl-conf.d/gl-app-wattline.conf}
NGINX=${WATTLINE_NGINX_BIN:-/usr/sbin/nginx}
NGINX_INIT=${WATTLINE_NGINX_INIT:-/etc/init.d/nginx}

[ -L "$LINK" ] || exit 0
[ "$(readlink "$LINK")" = "$SOURCE" ] || exit 0
rm -f "$LINK" || exit 1

if ! "$NGINX" -t; then
	ln -s "$SOURCE" "$LINK"
	echo 'gl-app-wattline: nginx validation failed; proxy removal was rolled back' >&2
	exit 1
fi

"$NGINX_INIT" reload
```

The scripts intentionally propagate validation/reload failures instead of ending with unconditional `exit 0`.

- [ ] **Step 5: Stage and validate the new package files**

In `package/Makefile`, extend `ipk-glapp` to create `stage-gl/etc/nginx/conf.d`, copy the location file, and mark both control scripts executable:

```make
	mkdir -p $(OUT)/stage-gl/www/views $(OUT)/stage-gl/usr/share/oui/menu.d \
		$(OUT)/stage-gl/usr/lib/oui-httpd/rpc $(OUT)/stage-gl/etc/nginx/conf.d
	cp gl-app-wattline/etc/nginx/conf.d/gl-app-wattline.locations $(OUT)/stage-gl/etc/nginx/conf.d/
	chmod +x $(OUT)/stage-gl/CONTROL/postinst $(OUT)/stage-gl/CONTROL/prerm
```

Add a `gl-app-wattline_*.ipk` branch to `package/check-ipk-metadata.sh` that requires `./postinst` and `./prerm` to be `-rwxr-xr-x` in `control.tar.gz.list`, and `./etc/nginx/conf.d/gl-app-wattline.locations` to be `-rw-r--r--` in `data.tar.gz.list`.

- [ ] **Step 6: Run the focused test and build the GL package**

Run:

```bash
sh package/tests/gl_nginx_proxy_test.sh
make -C package ipk-glapp
TAR="$(command -v gtar || command -v gnutar || echo tar)" \
  package/check-ipk-metadata.sh package/out/gl-app-wattline_*.ipk
```

Expected: the shell test prints `GL nginx proxy tests passed`; the package builds; metadata validation exits 0.

- [ ] **Step 7: Add the focused test to CI and release validation**

Add `package/tests/gl_nginx_proxy_test.sh` immediately after `package/tests/gl_contract_test.sh` in both `.github/workflows/ci.yml` and `.github/workflows/release.yml` package-test loops. Run:

```bash
sh package/tests/gl_nginx_proxy_test.sh
git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 8: Commit the packaged proxy**

```bash
git add package/gl-app-wattline package/Makefile package/check-ipk-metadata.sh \
  package/tests/gl_nginx_proxy_test.sh .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "feat: proxy Wattline through GL admin nginx"
```

---

### Task 2: Document the Route and Dual Authentication Boundary

**Files:**
- Modify: `package/tests/gl_nginx_proxy_test.sh`
- Create: `docs/admin-port-proxy.md`
- Modify: `docs/api.md`
- Modify: `docs/gl-panel-integration.md`

**Interfaces:**
- Consumes: `/wattline/` route and bearer-forwarding behavior produced by Task 1.
- Produces: the client/operator contract for direct, LAN-admin, and GoodCloud access, including an explicit unverified-remote status.

- [ ] **Step 1: Add failing documentation-contract assertions**

Before the success message in `package/tests/gl_nginx_proxy_test.sh`, add:

```sh
DOC="$ROOT/docs/admin-port-proxy.md"
API_DOC="$ROOT/docs/api.md"
PANEL_DOC="$ROOT/docs/gl-panel-integration.md"
need "$DOC" 'http://ROUTER/wattline/' 'admin-port base URL documentation'
need "$DOC" 'Authorization: Bearer TOKEN' 'proxied bearer contract'
need "$DOC" 'GoodCloud session plus the Wattline bearer token' 'dual-auth decision'
need "$DOC" 'GoodCloud relay verification: pending' 'remote verification status'
need "$API_DOC" 'http://ROUTER/wattline/' 'additional GL API base URL'
need "$PANEL_DOC" '/wattline/' 'as-built GL proxy route'
```

- [ ] **Step 2: Run the focused test and confirm the red state**

Run:

```bash
sh package/tests/gl_nginx_proxy_test.sh
```

Expected: FAIL because `docs/admin-port-proxy.md` does not exist.

- [ ] **Step 3: Write the operator-facing decision record**

Create `docs/admin-port-proxy.md` with these exact sections and facts:

```markdown
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
`/wattline/events` remains an SSE stream. GL nginx is built without its HTTP
cache module, so cache-module directives must not be used.

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
```

- [ ] **Step 4: Update the authoritative API and panel documents**

In `docs/api.md` under “Versioning and base URLs,” add:

```markdown
When `gl-app-wattline` is installed on GL.iNet SDK4 firmware,
`http://ROUTER/wattline/` is an additional reverse-proxied base: for example,
`/wattline/status` maps to `/api/v1/status`. It preserves the same request and
response contract, including bearer authentication and SSE. It does not replace
either direct listener.
```

In `docs/gl-panel-integration.md`, update the as-built summary to name the installed nginx source and symlink, the `/wattline/` mapping, unbuffered SSE, and the decision to require Wattline bearer authentication. Remove the stale statement that adding an nginx fragment is future work.

- [ ] **Step 5: Run documentation and existing GL contract tests**

Run:

```bash
sh package/tests/gl_nginx_proxy_test.sh
sh package/tests/gl_contract_test.sh
git diff --check
```

Expected: both tests pass and `git diff --check` is silent.

- [ ] **Step 6: Commit the documentation contract**

```bash
git add docs/admin-port-proxy.md docs/api.md docs/gl-panel-integration.md \
  package/tests/gl_nginx_proxy_test.sh
git commit -m "docs: specify GL admin-port API access"
```

---

### Task 3: Verify Locally and on a Live GL-X3000

**Files:**
- Modify after testing: `docs/admin-port-proxy.md`

**Interfaces:**
- Consumes: built `gl-app-wattline_<VERSION>_all.ipk`, router SSH at `192.168.8.1`, bootstrap bearer token read locally on the router.
- Produces: recorded LAN verification status and a clearly separated GoodCloud verification status.

- [ ] **Step 1: Run the complete local test suite**

Run:

```bash
go test ./...
for test in \
  package/tests/bluetooth-security-recovery_test.sh \
  package/tests/firewall-sync_test.sh \
  package/tests/vpn-firewall-repair_test.sh \
  package/tests/provisioning_test.sh \
  package/tests/luci_contract_test.sh \
  package/tests/gl_contract_test.sh \
  package/tests/gl_nginx_proxy_test.sh \
  package/tests/rtl8761b-artifacts_test.sh \
  package/tests/rtl8761b-driver_test.sh \
  package/tests/rtl8761b-lifecycle_test.sh; do
  sh "$test"
done
```

Expected: `go test` reports every package `ok` or `[no test files]`; every shell test exits 0.

- [ ] **Step 2: Build all gzip-ustar packages and validate metadata**

Run:

```bash
make -C package clean all
TAR="$(command -v gtar || command -v gnutar || echo tar)" \
  package/check-ipk-metadata.sh package/out/*.ipk
```

Expected: five `.ipk` files build, the `all` target's metadata check passes, and the explicit second metadata check exits 0.

- [ ] **Step 3: Capture a non-mutating router baseline**

On `192.168.8.1`, record without printing the bearer token:

```sh
opkg status gl-app-wattline
nginx -t
/etc/init.d/wattlined status
netstat -lntp | grep ':8377 '
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8377/api/v1/status
```

Expected: nginx configuration is valid, Wattline is running and listening on 8377, and the unauthenticated direct request returns `401`.

- [ ] **Step 4: Transfer and install only the GL integration package**

Use stdin transfer because the target Dropbear SCP implementation is unreliable:

```bash
IPK=$(printf '%s\n' package/out/gl-app-wattline_*.ipk)
ssh root@192.168.8.1 "cat > /tmp/$(basename "$IPK")" < "$IPK"
ssh root@192.168.8.1 "opkg install --force-reinstall /tmp/$(basename "$IPK") && nginx -t"
```

Expected: opkg succeeds, nginx validates, and no reboot is required. Stop immediately on any install or validation error; do not retry automatically.

- [ ] **Step 5: Verify authentication, path rewriting, and direct-port preservation**

Read the token into a shell variable on the router so it never appears in command output, then compare status payloads:

```sh
TOKEN=$(uci -q get wattline.main.token)
test -n "$TOKEN"
curl -sS -o /tmp/wattline-direct.json -w '%{http_code}\n' \
  -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8377/api/v1/status
curl -sS -o /tmp/wattline-proxy.json -w '%{http_code}\n' \
  -H "Authorization: Bearer $TOKEN" http://127.0.0.1/wattline/status
cmp /tmp/wattline-direct.json /tmp/wattline-proxy.json
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1/wattline/status
```

Expected: authenticated calls both return `200` with byte-identical JSON; the unauthenticated proxy call returns `401`; `:8377` remains listening.

- [ ] **Step 6: Verify SSE is live and unbuffered**

Run on the router:

```sh
TOKEN=$(uci -q get wattline.main.token)
timeout 15 curl -sS -N -D /tmp/wattline-events.headers \
  -H "Authorization: Bearer $TOKEN" http://127.0.0.1/wattline/events \
  > /tmp/wattline-events.body || [ "$?" -eq 124 ]
grep -i '^Content-Type: text/event-stream' /tmp/wattline-events.headers
grep -Eq '^(event:|data:)' /tmp/wattline-events.body
```

Expected: the response begins within 15 seconds, has `text/event-stream`, contains SSE framing, and remains open until `timeout` ends it.

- [ ] **Step 7: Verify clean removal and reinstall**

Run:

```sh
opkg remove gl-app-wattline
test ! -e /etc/nginx/gl-conf.d/gl-app-wattline.conf
nginx -t
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8377/api/v1/status
```

Expected: removal succeeds, the managed link is absent, nginx remains valid, and direct port 8377 still returns unauthenticated `401`. Reinstall the same `.ipk`, rerun `nginx -t`, and repeat Steps 5–6 so the router is left in the intended installed state.

- [ ] **Step 8: Record hardware and GoodCloud status**

Replace the pending LAN line in `docs/admin-port-proxy.md` with the tested router model, firmware version, date, and pass/fail observations. If a browser-authenticated GoodCloud URL is available, verify `/wattline/status` with and without the bearer and verify `/wattline/events` streaming; otherwise leave `GoodCloud relay verification: pending` unchanged and explain that the remote session was unavailable.

- [ ] **Step 9: Re-run verification and commit only the evidence update**

```bash
sh package/tests/gl_nginx_proxy_test.sh
git diff --check
git add docs/admin-port-proxy.md
git commit -m "docs: record GL admin proxy verification"
git status --short --branch
```

Expected: tests pass, the branch is clean, and no router credentials, bearer tokens, cookies, or GoodCloud secrets appear in the diff or commit.
