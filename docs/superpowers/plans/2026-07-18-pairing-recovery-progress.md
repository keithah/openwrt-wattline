# Pairing Recovery and Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an explicit orphaned-bond recovery operation and detailed live pairing progress to wattlined, the GL panel, and LuCI.

**Architecture:** Extend the existing asynchronous pairing manager with a bounded in-memory progress timeline and a recovery flag. Reuse the BlueZ pairer for unconditional local-object removal, rediscovery, PIN exchange, explicit `Paired: true` confirmation, trust, and the existing connector handshake; expose the state through the versioned API and render it identically in both router panels.

**Tech Stack:** Go 1.22, godbus/dbus v5, BlueZ, Vue 2 GL OUI bundle, LuCI JavaScript, Node behavior harnesses, OpenWrt gzip-tar IPKs.

## Global Constraints

- The Link-Power protocol has no device-side erase-bonds command; UI copy must say the operation clears the router's stale pairing and requests a replacement bond.
- Preserve all existing pairing API fields and endpoint behavior.
- Never persist the PIN or MAC until the protected handshake succeeds.
- Never expose PINs, bearer tokens, key material, or raw D-Bus payloads in progress events.
- Keep at most 32 events for the latest operation, in memory only.
- Do not create a second BLE session or modify Swift code.
- Package version is 0.1.2.

---

### Task 1: Pairing progress state machine

**Files:**
- Modify: `internal/ble/pairing.go`
- Modify: `internal/ble/pairing_test.go`

**Interfaces:**
- Produces: `PairingPhase`, `PairingEvent`, additive `PairingStatus` progress fields, `PairProgress`, and `StartRecover(mac, pin string) error`.
- Changes: `PairOps.Pair` accepts `recover bool` and `PairProgress`; `PairingDeps.WaitConnected` accepts `PairProgress`.

- [ ] **Step 1: Write failing manager tests**

Add tests that use a deterministic clock and assert the recovery phase sequence, 32-event cap, duplicate-phase suppression, elapsed-time freezing, failure cleanup, no persistence before verification, and `StartRecover` busy policy. The fake pair operation records the `recover` flag and reports `locating_device`, `exchanging_pin`, and `confirming_bond`.

- [ ] **Step 2: Run the focused tests RED**

Run: `go test ./internal/ble -run 'TestPairingProgress|TestRecover' -count=1`

Expected: build failures for the new progress types and `StartRecover`.

- [ ] **Step 3: Implement progress storage and recovery orchestration**

Add stable phase constants from the design, a 32-entry event slice, operation timestamps, and a `setPhase` helper that appends only when the phase changes. `Status()` computes `elapsed_ms` from the injected clock while busy and from the frozen finish time afterward. Implement `StartPair` and `StartRecover` through one private `startPair(mac, pin string, recover bool)` path.

- [ ] **Step 4: Run manager tests GREEN**

Run: `go test ./internal/ble -count=1`

Expected: all BLE package tests pass.

- [ ] **Step 5: Commit the manager slice**

```bash
git add internal/ble/pairing.go internal/ble/pairing_test.go
git commit -m "Expose detailed pairing progress"
```

### Task 2: BlueZ forced recovery and bond confirmation

**Files:**
- Modify: `internal/ble/bluez.go`
- Modify: `internal/ble/bluez_test.go`

**Interfaces:**
- Consumes: `PairOps.Pair(mac string, recover bool, report PairProgress) error`.
- Produces: unconditional `RemoveDevice` for recovery and an explicit `Device1.Paired == true` success gate.

- [ ] **Step 1: Write failing BlueZ policy tests**

Add tests for `shouldRemoveBeforePair(recover, paired bool) bool` and `bondConfirmed(value any) bool`. Assert recovery always requests removal, ordinary pair removes only a confirmed local bond, `AlreadyExists` with `Paired: false` fails, `Paired: true` succeeds, and every reported message excludes the supplied PIN.

- [ ] **Step 2: Run the focused tests RED**

Run: `go test ./internal/ble -run 'TestPairPreparation|TestBondConfirmation' -count=1`

Expected: failures because the policy helpers and new Pair signature do not exist.

- [ ] **Step 3: Implement the BlueZ recovery sequence**

Report curated phases around existing operations. For recovery, call `RemoveDevice` before rediscovery even when BlueZ says `Paired: no`. After `Device1.Pair` and its existing cancel/retry path, read `org.bluez.Device1.Paired`; return a phase-specific error unless it is boolean true. Keep `DoesNotExist` removal idempotent and keep raw D-Bus data out of messages.

- [ ] **Step 4: Run BLE tests GREEN**

Run: `go test ./internal/ble -count=1`

Expected: all BLE tests pass, including the GL-X3000 discovery regression.

- [ ] **Step 5: Commit the BlueZ slice**

```bash
git add internal/ble/bluez.go internal/ble/bluez_test.go
git commit -m "Recover orphaned BlueZ bonds"
```

### Task 3: Recovery API and connector-derived progress

**Files:**
- Modify: `cmd/wattlined/main.go`
- Modify: `cmd/wattlined/main_test.go`
- Modify: `internal/api/pairing.go`
- Modify: `internal/api/pairing_test.go`
- Modify: `internal/api/server.go`
- Modify: `docs/api.md`

**Interfaces:**
- Produces: authenticated `POST /api/v1/pairing/recover` with the same `{mac,pin}` validation and `202/400/409/502` policy as pair.
- Consumes: existing state-store connection phases `connecting`, `handshaking`, and `ready`.

- [ ] **Step 1: Write failing endpoint and verifier tests**

Add table tests for valid recovery, malformed MAC/PIN, nonempty unknown fields, missing auth, busy policy, and unsupported pairing. Add a main-package test that feeds connection snapshots and asserts progress transitions from `reconnecting` to `verifying_handshake` before success.

- [ ] **Step 2: Run API tests RED**

Run: `go test ./internal/api ./cmd/wattlined -run 'Recover|PairingConnectionProgress' -count=1`

Expected: 404 or build failures for the new route and callback.

- [ ] **Step 3: Implement route and verifier callback**

Register `POST /api/v1/pairing/recover`, decode with the existing bounded strict JSON helper, call `StartRecover`, and return `202 {"status":"pairing"}`. Change `WaitConnected` to report `reconnecting` for connector `connecting`, `verifying_handshake` for `handshaking`, and success only for `ready` plus connected.

- [ ] **Step 4: Update the API contract**

Document the recovery request/reply, all additive status fields, stable phase strings, 32-event limit, error policy, and the device-side erase limitation in `docs/api.md`.

- [ ] **Step 5: Run API and daemon tests GREEN**

Run: `go test ./internal/api ./cmd/wattlined -count=1`

Expected: all tests pass.

- [ ] **Step 6: Commit the API slice**

```bash
git add cmd/wattlined internal/api docs/api.md
git commit -m "Add pairing recovery API"
```

### Task 4: GL panel progress stepper and recovery control

**Files:**
- Modify: `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`
- Modify: `package/tests/gl_contract_test.sh`

**Interfaces:**
- Consumes: additive pairing status and `POST /pairing/recover`.
- Produces: accessible live stepper, elapsed time, expandable technical events, and confirmed **Clear stale pairing & retry** action.

- [ ] **Step 1: Write failing GL behavior tests**

In the existing extracted-component harness, assert phase order and labels, completed/active/failed rendering, elapsed-time text, collapsed technical details, `aria-live` only on the current message, exact recovery confirmation copy, request body, busy suppression, and absence of PIN/token text in event rendering.

- [ ] **Step 2: Run GL tests RED**

Run: `sh package/tests/gl_contract_test.sh`

Expected: failures for the missing recovery route, stepper, and button.

- [ ] **Step 3: Implement the GL pairing presentation**

Add pure phase-order/status helpers and render the stepper from server truth. Render events inside a native expandable details element, format event timestamps locally, and send recovery once after confirmation using the selected or status target MAC plus the current PIN.

- [ ] **Step 4: Run GL tests GREEN**

Run: `sh package/tests/gl_contract_test.sh`

Expected: GL behavior and contract tests pass with valid JavaScript syntax.

- [ ] **Step 5: Commit the GL slice**

```bash
git add package/gl-app-wattline package/tests/gl_contract_test.sh
git commit -m "Show pairing recovery progress in GL panel"
```

### Task 5: LuCI progress stepper and recovery control

**Files:**
- Modify: `package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js`
- Create: `package/luci-app-wattline/www/luci-static/resources/wattline/pairing_progress.js`
- Modify: `package/tests/luci_behavior_test.js`
- Modify: `package/tests/luci_contract_test.sh`

**Interfaces:**
- Consumes: the same pairing status and recovery endpoint as GL.
- Produces: behavior and copy parity with the GL panel.

- [ ] **Step 1: Write failing LuCI behavior tests**

Extend the fake-DOM harness with the same phase, accessibility, technical-details, confirmation, exact-once request, and secret-redaction assertions used for GL.

- [ ] **Step 2: Run LuCI tests RED**

Run: `node package/tests/luci_behavior_test.js && sh package/tests/luci_contract_test.sh`

Expected: failures for the missing recovery UI and route.

- [ ] **Step 3: Implement LuCI parity**

Render the same ordered phases and messages, preserve the selected MAC and PIN across polling, use `window.confirm` with the contract text, and invoke `POST /pairing/recover` once. Keep technical details collapsed and current-message announcements accessible.

- [ ] **Step 4: Run LuCI tests GREEN**

Run: `node package/tests/luci_behavior_test.js && sh package/tests/luci_contract_test.sh`

Expected: both suites pass.

- [ ] **Step 5: Commit the LuCI slice**

```bash
git add package/luci-app-wattline package/tests/luci_behavior_test.js package/tests/luci_contract_test.sh
git commit -m "Show pairing recovery progress in LuCI"
```

### Task 6: Package v0.1.2 and verify the orphaned-bond recovery live

**Files:**
- Modify: `package/Makefile`
- Modify: `package/*/CONTROL/control`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `package/tests/rtl8761b-lifecycle_test.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: five consistent v0.1.2 IPKs and feed metadata.
- Deploys: updated wattlined, GL panel, and LuCI panel to `100.87.232.42`.

- [ ] **Step 1: Move version assertions to 0.1.2 and verify RED**

Update CI/release-inventory and RTL lifecycle expectations, then run `sh package/tests/rtl8761b-lifecycle_test.sh`.

Expected: failure while source control metadata is still 0.1.1.

- [ ] **Step 2: Bump all package metadata and current README examples**

Set the Makefile default and every package control version to 0.1.2. Update current release commands and examples without rewriting historical release references.

- [ ] **Step 3: Run the full host gate and clean feed build**

Run `go test -count=1 ./...`, `go vet ./...`, every package shell/Node behavior test, `make -C package clean feed`, and `sh package/tests/release-inventory_test.sh package/out 0.1.2`.

Expected: every command exits zero and the feed contains exactly five IPKs.

- [ ] **Step 4: Preserve-check and install affected packages**

Capture hashes for the UCI token stream, token store, certificate, and key without printing secrets. Stream and hash-verify the v0.1.2 wattlined, GL, and LuCI IPKs, install with opkg, and prove the credential hashes remain unchanged.

- [ ] **Step 5: Exercise the live orphaned recovery**

With the currently observed `Paired: no`, `Trusted: yes`, and Link-Power advertising, call recovery and poll status. Capture every phase/event. Success requires BlueZ `Paired: yes`, `Trusted: yes`, protected handshake completion, UCI pairing persistence, and connected telemetry. If the peripheral rejects replacement, retain the detailed failure evidence and do not claim device-side clearing.

- [ ] **Step 6: Commit the patch release metadata**

```bash
git add package .github/workflows README.md
git commit -m "Prepare v0.1.2 packages"
```

- [ ] **Step 7: Finish and integrate**

Use the finishing-a-development-branch workflow after fresh verification. Do not push or tag unless the user explicitly selects an option that authorizes it.
