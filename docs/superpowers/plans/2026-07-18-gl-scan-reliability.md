# GL Panel Scan Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make GL-panel BLE scans work when the browser rejects wattlined's self-signed certificate and when BlueZ already has discovery in progress.

**Architecture:** The GL API client retains the listener proven by a safe authenticated request and sends each mutation once through that listener. The Linux BlueZ backend classifies the canonical discovery-in-progress D-Bus error, plus observed legacy message forms, as a shared benign scan.

**Tech Stack:** Vue 2 browser bundle, Node behavior harness, Go 1.22, godbus/dbus v5, BlueZ D-Bus, OpenWrt gzip-tar IPKs.

## Global Constraints

- Mutations must never be retried after a connection error.
- HTTPS remains enabled and preferred until a request proves another listener reachable.
- Only BlueZ discovery-in-progress errors are benign; unrelated errors remain failures.
- Package version is 0.1.1.
- Preserve UCI configuration, tokens, TLS identity, rules, webhooks, SSE, and all Swift code.

---

### Task 1: Retain the proven GL API listener

**Files:**
- Modify: `package/tests/gl_contract_test.sh`
- Modify: `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`

**Interfaces:**
- Consumes: `apiClient(config, token)` and its `json(method, path, body, extra)` method.
- Produces: a closure-private preferred endpoint updated by every completed HTTP response.

- [ ] **Step 1: Write the failing transport regression tests**

Extend `transportTests()` so the existing HTTPS-failure/HTTP-success GET is followed by a scan mutation and assert its only URL is HTTP. Add a case where an HTTP `403` response becomes the preferred reachable listener even though `client.json()` rejects the application response.

```javascript
await client.json('GET', '/device');
await client.json('POST', '/pairing/scan');
assert.deepStrictEqual(calls.map((item) => item[0]), [
  'https://router.lan:8378/api/v1/device',
  'http://router.lan:8377/api/v1/device',
  'http://router.lan:8377/api/v1/pairing/scan'
]);
```

- [ ] **Step 2: Run the test and verify RED**

Run: `sh package/tests/gl_contract_test.sh`

Expected: FAIL because the scan POST still targets `https://router.lan:8378`.

- [ ] **Step 3: Implement sticky listener selection**

In `apiClient`, keep `preferred` as the endpoint string. Safe requests try the preferred endpoint first, then each other configured endpoint once. Record `preferred` as soon as `fetch` resolves to an HTTP response, before canonical response handling. Mutations use `[preferred || endpoints[0]]` and retain the existing single-attempt behavior.

- [ ] **Step 4: Run transport and syntax tests GREEN**

Run: `sh package/tests/gl_contract_test.sh`

Expected: `GL contract tests passed` with no JavaScript syntax errors.

- [ ] **Step 5: Commit the transport fix**

```bash
git add package/tests/gl_contract_test.sh package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js
git commit -m "Fix GL panel listener selection"
```

### Task 2: Accept BlueZ discovery already in progress

**Files:**
- Create: `internal/ble/bluez_test.go`
- Modify: `internal/ble/bluez.go`

**Interfaces:**
- Consumes: errors returned by `org.bluez.Adapter1.StartDiscovery`.
- Produces: `discoveryInProgress(error) bool`, used only by `startDiscovery`.

- [ ] **Step 1: Write the failing table test**

Add a Linux test whose cases include `dbus.NewError("org.bluez.Error.InProgress", []interface{}{"Operation already in progress"})`, plain errors containing `InProgress`, the GL-X3000 text, and an unrelated `org.bluez.Error.Failed`. Assert the first three are true and the unrelated error is false.

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./internal/ble -run TestDiscoveryInProgress -count=1`

Expected: build failure because `discoveryInProgress` is undefined.

- [ ] **Step 3: Implement the narrow classifier**

Use `errors.As` to inspect `dbus.Error.Name`, then fall back to the two observed message forms. Replace the current `strings.Contains(call.Err.Error(), "InProgress")` check with the helper. Keep the no-op stop function when sharing another discovery session.

- [ ] **Step 4: Run BLE tests GREEN**

Run: `go test ./internal/ble -count=1`

Expected: all BLE tests pass.

- [ ] **Step 5: Commit the BlueZ fix**

```bash
git add internal/ble/bluez.go internal/ble/bluez_test.go
git commit -m "Handle active BlueZ discovery scans"
```

### Task 3: Build the v0.1.1 patch packages

**Files:**
- Modify: `package/Makefile`
- Modify: `package/*/CONTROL/control`
- Modify: `package/tests/rtl8761b-lifecycle_test.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: the package builder's `VERSION` value.
- Produces: five IPKs and two feed files whose filenames and control metadata consistently report 0.1.1.

- [ ] **Step 1: Update the version assertions first**

Change the release inventory argument and RTL lifecycle expected control version from `0.1.0` to `0.1.1`, then run `sh package/tests/rtl8761b-lifecycle_test.sh`.

Expected: FAIL because source control metadata remains 0.1.0.

- [ ] **Step 2: Bump package sources and documentation**

Set the Makefile default and every package `CONTROL/control` version to `0.1.1`. Update README commands and default-version examples that describe the current release; retain explicitly historical examples only where they are not current instructions.

- [ ] **Step 3: Run package lifecycle tests GREEN**

Run: `sh package/tests/rtl8761b-lifecycle_test.sh`

Expected: `RTL8761B lifecycle tests passed`.

- [ ] **Step 4: Build and inspect the feed**

Run `make -C package clean feed`, then `sh package/tests/release-inventory_test.sh package/out 0.1.1`.

Expected: exactly five 0.1.1 IPKs, `Packages`, and `Packages.gz`; metadata checks pass.

- [ ] **Step 5: Commit the patch version**

```bash
git add package/Makefile package/*/CONTROL/control package/tests/rtl8761b-lifecycle_test.sh .github/workflows/ci.yml README.md
git commit -m "Prepare v0.1.1 packages"
```

### Task 4: Verify and install on the GL-X3000

**Files:**
- Test: all Go and package test suites
- Deploy: `package/out/wattlined_0.1.1_aarch64_cortex-a53.ipk`
- Deploy: `package/out/gl-app-wattline_0.1.1_all.ipk`

**Interfaces:**
- Consumes: v0.1.1 IPKs from Task 3 and router `100.87.232.42`.
- Produces: live daemon and GL panel with preserved credentials and a working scan flow.

- [ ] **Step 1: Run the full host verification gate**

Run `go test -count=1 ./...`, `go vet ./...`, every package shell test with the release inventory test receiving `package/out 0.1.1`, and both package Node behavior tests.

Expected: every command exits zero.

- [ ] **Step 2: Capture persistence hashes and transfer affected IPKs**

Record SHA-256 hashes for the UCI token stream, token store, certificate, and key without printing secrets. Stream the two IPKs over SSH using `cat > /tmp/<name>` and verify remote SHA-256 hashes equal local hashes.

- [ ] **Step 3: Install affected packages**

Run `opkg install --force-reinstall` on wattlined and gl-app-wattline. Confirm both report `Version: 0.1.1`, wattlined is running, the API returns authenticated 200 on HTTP and HTTPS, and credential hashes are unchanged.

- [ ] **Step 4: Exercise the original failure path**

Use the GL bundle's equivalent sequence: make a safe authenticated GET fall back from untrusted HTTPS to HTTP, then send one scan POST through HTTP. Confirm `202`, poll pairing status through `scanning` to `idle`, and assert it never reports `Operation already in progress`.

- [ ] **Step 5: Verify discovery and repository state**

Confirm Link-Power advertisements appear when the device is advertising; otherwise report hardware discovery as unavailable rather than claiming it. Run `git diff --check`, `git status -sb`, and inspect the final commits before any push or release action.
