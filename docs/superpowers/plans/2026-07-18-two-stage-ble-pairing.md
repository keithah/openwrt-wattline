# Two-stage BLE Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a two-stage, user-entered BLE PIN flow to wattlined so the Link-Power LCD code remains visible until the GUI submits it, with one attempt and no automatic retry.

**Architecture:** Add a concurrency-safe passkey prompt broker beneath the existing BlueZ agent. Extend the existing `ble.Pairing` state machine with an interactive start/submit/cancel path, then expose additive authenticated HTTP routes. Update LuCI and GL to use the new path while retaining the existing direct pairing routes as compatibility aliases.

**Tech Stack:** Go 1.x, godbus D-Bus, BlueZ Agent1, Go `net/http`, LuCI JavaScript, GL SDK4 JavaScript, existing Go and JS test harnesses, OpenWrt package Makefiles.

---

## File map

- Modify `internal/ble/agent.go`: connect BlueZ Agent1 callbacks to a prompt broker; keep direct PIN behavior for compatibility.
- Modify `internal/ble/unsupported.go`: keep the non-Linux `RegisterPairingAgent` signature in sync.
- Create `internal/ble/passkey_prompt.go`: broker state, deadline, submit/cancel operations, and no-secret status callback.
- Create `internal/ble/passkey_prompt_test.go`: broker red/green unit tests.
- Modify `internal/ble/pairing.go`: interactive lifecycle, `awaiting_pin` status, deadline fields, submit/cancel methods, and single-attempt cleanup.
- Modify `internal/ble/pairing_test.go`: interactive state-machine and no-retry tests.
- Modify `internal/ble/bluez.go`: arm interactive mode and remove the unconditional retry from the interactive path.
- Modify `internal/ble/bluez_test.go`: single-call/no-retry behavior tests.
- Modify `cmd/wattlined/main.go`: construct one broker, register it with the agent, and inject it into `PairingDeps`.
- Modify `internal/api/pairing.go`: request-code, submit-pin, and cancel handlers with exact validation/error mapping.
- Modify `internal/api/server.go`: register the additive routes.
- Modify `internal/api/pairing_test.go`: endpoint contract and status schema tests.
- Modify `package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js`: two-stage LuCI controls, countdown, submit/cancel, and recovery.
- Modify `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`: equivalent GL controls and routes.
- Modify `package/tests/luci_behavior_test.js`: LuCI interaction assertions.
- Modify `package/tests/gl_contract_test.sh`: GL route/body/button assertions (its embedded Node test exercises the GL component).
- Modify `docs/api.md`: document additive pairing routes and status fields.
- Modify `docs/gl-x3000-verification.md`: replace direct-PIN verification with the interactive checklist.
- Modify package version/changelog files only after tests pass; keep release packaging separate from target verification.

## Task 1: Add the passkey prompt broker

**Files:**
- Create: `internal/ble/passkey_prompt.go`
- Test: `internal/ble/passkey_prompt_test.go`

- [ ] **Step 1: Write the failing tests.** Define a broker-facing callback contract in the test first. Cover:

```go
func TestPromptBlocksUntilPINIsSubmitted(t *testing.T) {

    prompt := NewPasskeyPrompt(25 * time.Second)

    ready := make(chan struct{})
    result := make(chan promptResult, 1)
    go func() {
        close(ready)
        pin, err := prompt.Wait(func() {})
        result <- promptResult{pin: pin, err: err}
    }()
    <-ready

    select {
    case <-result:
        t.Fatal("prompt returned before submit")
    case <-time.After(20 * time.Millisecond):
    }
    if err := prompt.Submit("020555"); err != nil { t.Fatal(err) }
    got := <-result
    if got.err != nil || got.pin != "020555" { t.Fatalf("got %#v", got) }
}

func TestPromptTimesOutAndDoesNotExposePIN(t *testing.T) {

    prompt := NewPasskeyPrompt(5 * time.Millisecond)
    _, err := prompt.Wait(func() {})
    if !errors.Is(err, ErrPasskeyTimeout) { t.Fatalf("err = %v", err) }
    if err := prompt.Submit("020555"); !errors.Is(err, ErrPasskeyNotWaiting) {
        t.Fatalf("late submit = %v", err)
    }
}

func TestPromptCancelUnblocksWaiterAndRejectsDuplicates(t *testing.T) {

    prompt := NewPasskeyPrompt(time.Second)
    result := make(chan error, 1)
    go func() { _, err := prompt.Wait(func() {}); result <- err }()
    prompt.Cancel()
    if !errors.Is(<-result, ErrPasskeyCanceled) { t.Fatal("waiter was not canceled") }
    if err := prompt.Submit("020555"); !errors.Is(err, ErrPasskeyNotWaiting) { t.Fatal(err) }
}
```

Add table tests rejecting non-six-digit or non-ASCII input, duplicate `Submit`, and a second concurrent `Wait`. Use a fake clock only if a deterministic deadline is needed; do not put PINs in status strings.

- [ ] **Step 2: Run the focused tests and verify the expected failure.**

Run: `go test ./internal/ble -run 'TestPrompt' -count=1`

Expected: compilation failure because `NewPasskeyPrompt`, result/errors, and broker methods do not exist.

- [ ] **Step 3: Implement the minimal broker.** Define `ErrPasskeyTimeout`, `ErrPasskeyCanceled`, `ErrPasskeyNotWaiting`, a `PasskeyPrompt` with a mutex, one result channel, a `Wait(onWaiting func()) (string,error)` method, `Submit(string) error`, and `Cancel()`. Validate exactly six ASCII digits before sending. `Wait` must invoke `onWaiting` once before blocking and must close/reset all state on every return. Expose only `Waiting`, `Deadline`, and boolean state to callers; never expose the stored PIN after delivery.

- [ ] **Step 4: Run the focused tests and then the package tests.**

Run: `go test ./internal/ble -run 'TestPrompt' -count=1 && go test ./internal/ble -count=1`

Expected: PASS with no race warnings.

- [ ] **Step 5: Commit.**

Run: `git add internal/ble/passkey_prompt.go internal/ble/passkey_prompt_test.go && git commit -m "ble: add bounded passkey prompt broker"`

## Task 2: Integrate the broker with the agent and pairing state machine

**Files:**
- Modify: `internal/ble/agent.go`, `internal/ble/pairing.go`, `internal/ble/bluez.go`
- Test: `internal/ble/pairing_test.go`, `internal/ble/bluez_test.go`

- [ ] **Step 1: Add failing pairing-manager tests.** Extend the existing `pairingHarness` with a `prompt *PasskeyPrompt` and add a `newInteractiveHarness` helper that injects it into `PairingDeps`. Assert the following concrete behaviors:

```go
func TestStartInteractivePairReportsAwaitingPIN(t *testing.T) {
    h := newInteractiveHarness(&fakeOps{})
    if err := h.p.StartInteractive("DC:04:5A:EB:72:2B", false); err != nil { t.Fatal(err) }
    waitFor(t, "awaiting pin", func() bool { return h.p.Status().Phase == PhaseAwaitingPIN })
    st := h.p.Status()
    if !st.PinRequired || st.PinDeadline.IsZero() { t.Fatalf("status = %+v", st) }
}

func TestSubmitPINContinuesTheExistingPairAttempt(t *testing.T) {
    h := newInteractiveHarness(&fakeOps{})
    if err := h.p.StartInteractive("DC:04:5A:EB:72:2B", false); err != nil { t.Fatal(err) }
    waitFor(t, "awaiting pin", func() bool { return h.p.Status().PinRequired })
    if err := h.p.SubmitPIN("020555"); err != nil { t.Fatal(err) }
    waitFor(t, "paired stage", func() bool { return h.p.Status().Stage == StagePaired })
    if calls := h.ops.got(); len(calls) != 2 || calls[0] != "pair false DC:04:5A:EB:72:2B" || calls[1] != "trust DC:04:5A:EB:72:2B" {
        t.Fatalf("ops calls = %v", calls)
    }
}

func TestInteractivePairFailureDoesNotRetry(t *testing.T) {
    ops := &fakeOps{pairErr: errors.New("confirm value failed")}
    h := newInteractiveHarness(ops)
    if err := h.p.StartInteractive("DC:04:5A:EB:72:2B", false); err != nil { t.Fatal(err) }
    waitFor(t, "awaiting pin", func() bool { return h.p.Status().PinRequired })
    if err := h.p.SubmitPIN("020555"); err != nil { t.Fatal(err) }
    waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
    if calls := ops.got(); len(calls) != 1 { t.Fatalf("pair call count = %d", len(calls)) }
}

func TestInteractiveCancelCleansUpAndResumesConnector(t *testing.T) {
    h := newInteractiveHarness(&fakeOps{})
    if err := h.p.StartInteractive("DC:04:5A:EB:72:2B", false); err != nil { t.Fatal(err) }
    waitFor(t, "awaiting pin", func() bool { return h.p.Status().PinRequired })
    if err := h.p.Cancel(); err != nil { t.Fatal(err) }
    waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
    h.mu.Lock(); defer h.mu.Unlock()
    if h.resumed == 0 { t.Fatal("connector was not resumed") }
}
```

The fake must block until the test submits a PIN and record the `recover` flag and call count. Assert that persistence is not called before reconnect verification.

- [ ] **Step 2: Run the tests to verify they fail for missing interactive APIs.**

Run: `go test ./internal/ble -run 'Test(StartInteractive|SubmitPIN|InteractivePair|InteractiveCancel)' -count=1`

Expected: compile failure for missing interactive dependency/methods and status fields.

- [ ] **Step 3: Implement agent integration.** Change `RegisterPairingAgent` to accept a `*PasskeyPrompt` (and update the non-Linux stub and `cmd/wattlined/main.go`). Give the registered agent that broker reference. `RequestPasskey` and `RequestPinCode` use direct configured PINs in compatibility mode; interactive mode calls broker `Wait`, reports `awaiting_pin`, and returns the submitted numeric value or a BlueZ cancellation/rejection error. Add an idempotent cancel path and ensure the broker is canceled by the unregister function.

- [ ] **Step 4: Implement pairing-manager interactive methods.** Add `PhaseAwaitingPIN`, `PinRequired`, and `PinDeadline` to `PairingStatus`. Add `StartInteractive(mac string, recover bool) error`, `SubmitPIN(pin string) error`, and `Cancel() error`. Arm the broker before `Device1.Pair`; set the phase callback when BlueZ asks; switch to `exchanging_pin` after submit; use the existing trust/reconnect/persist path; clear the broker and resume the connector on every error. Keep the existing `StartPair` and `StartRecover` aliases but route them through the same single-attempt path.

- [ ] **Step 5: Implement BlueZ single-attempt behavior.** Keep `PairOps.Pair`'s signature stable and remove the existing `CancelPairing` + second `Pair` retry from `bluezPairer.Pair`; every caller therefore gets exactly one `Device1.Pair` call. Preserve stale-object removal and post-pair `Paired: yes` confirmation. The pairing manager's explicit retry boundary is the next user click, never a goroutine retry.

- [ ] **Step 6: Run BLE tests and race tests.**

Run: `go test ./internal/ble -count=1 -race`

Expected: PASS; interactive failure reports one `Pair` call, and no PIN appears in test output.

- [ ] **Step 7: Commit.**

Run: `git add internal/ble/agent.go internal/ble/passkey_prompt.go internal/ble/pairing.go internal/ble/pairing_test.go internal/ble/bluez.go internal/ble/bluez_test.go && git commit -m "ble: support interactive single-attempt pairing"`

## Task 3: Expose the HTTP contract

**Files:**
- Modify: `internal/api/pairing.go`, `internal/api/server.go`
- Test: `internal/api/pairing_test.go`

- [ ] **Step 1: Write failing API tests.** Add table tests for authenticated `POST /api/v1/pairing/request-code`, `POST /api/v1/pairing/submit-pin`, and `POST /api/v1/pairing/cancel`. The core success assertions should have this shape:

```go
if w := do(t, h, http.MethodPost, "/api/v1/pairing/request-code", "tok", `{"mac":"DC:04:5A:EB:72:2B"}`); w.Code != http.StatusAccepted { t.Fatal(w.Code) }
waitStage(t, h, "pairing")
submitted := do(t, h, http.MethodPost, "/api/v1/pairing/submit-pin", "tok", `{"pin":"020555"}`)
if submitted.Code != http.StatusAccepted { t.Fatal(submitted.Code) }
if strings.Contains(submitted.Body.String(), "020555") { t.Fatal("PIN echoed") }
```

Also assert exact JSON, strict-body rejection, six-digit validation, busy state, no-prompt state (`409 pairing_pin_not_requested`), and that status includes `pin_required`/`pin_deadline` only while waiting.

- [ ] **Step 2: Run the API tests to verify they fail.**

Run: `go test ./internal/api -run 'TestPairing(Interactive|Submit|Cancel|RequestCode)' -count=1`

Expected: route-not-found or missing-handler failures.

- [ ] **Step 3: Register and implement handlers.** Add routes in the same authenticated pairing route table. Decode `{mac,recover}` for request-code, exact `{pin}` for submit, and no body for cancel. Map broker/state errors to canonical `invalid_request`, `operation_in_progress`, `pairing_pin_not_requested`, `capability_unsupported`, and `ble_operation_failed` envelopes. Keep `/pair` and `/recover` unchanged as compatibility aliases.

- [ ] **Step 4: Run focused and full API tests.**

Run: `go test ./internal/api -count=1`

Expected: PASS.

- [ ] **Step 5: Commit.**

Run: `git add internal/api/pairing.go internal/api/server.go internal/api/pairing_test.go && git commit -m "api: expose interactive BLE pairing"`

## Task 4: Update LuCI and GL pairing flows

**Files:**
- Modify: `package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js`
- Modify: `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`
- Test: `package/tests/luci_behavior_test.js`, `package/tests/gl_contract_test.sh`

- [ ] **Step 1: Add failing behavior tests.** Assert that after scan/select the UI renders **Show pairing code**, calls `POST /pairing/request-code` with `{mac,recover:false}`, displays the six-digit input prefilled `020555` only after `pin_required`, calls `/pairing/submit-pin` with the entered value, and calls `/pairing/cancel` exactly once. Add recovery coverage with `{recover:true}`. Assert no `/pairing/pair` or second request is made after a failed status.

- [ ] **Step 2: Run the behavior tests and verify failure.**

Run: `node --test package/tests/luci_behavior_test.js && sh package/tests/gl_contract_test.sh`

Expected: assertions fail because the old immediate-PIN controls and routes are still present.

- [ ] **Step 3: Implement LuCI controls.** Preserve scan/device selection and the existing stepper. Replace the active immediate Pair action with request-code. When status is `awaiting_pin`, render the numeric input, default value, countdown from `pin_deadline`, submit, cancel, and an `aria-live` message. Make recovery call request-code with `recover:true` after confirmation. On terminal failure, leave details visible and require a fresh button click.

- [ ] **Step 4: Implement the same GL flow.** Keep GL's existing polling and error presentation, but use the additive routes and the same single-attempt state transitions. Do not change Swift or shared protocol code.

- [ ] **Step 5: Run both behavior suites.**

Run: `node --test package/tests/luci_behavior_test.js && sh package/tests/gl_contract_test.sh`

Expected: PASS.

- [ ] **Step 6: Commit.**

Run: `git add package/luci-app-wattline package/gl-app-wattline package/tests && git commit -m "ui: make BLE pairing an explicit two-stage flow"`

## Task 5: Document the API and verification procedure

**Files:**
- Modify: `docs/api.md`
- Modify: `docs/gl-x3000-verification.md`

- [ ] **Step 1: Add documentation assertions/checklist items.** Ensure the docs contain exact request/reply JSON, auth, error codes, status fields, timeout behavior, no-retry policy, and the distinction between default and LCD PINs.

- [ ] **Step 2: Update `docs/api.md`.** Add the three routes and additive status schema without deleting the legacy pairing routes. State that PINs are never echoed or logged and that interactive pairing requires a new explicit attempt after failure.

- [ ] **Step 3: Update the GL-X3000 checklist.** Include durable-bond proof (`Paired`, LTK, authenticated), trusted state, protected handshake, restart reconnect, wrong-PIN one-attempt behavior, timeout, and cancel cleanup.

- [ ] **Step 4: Run documentation checks and commit.**

Run: `git diff --check && rg -n 'request-code|submit-pin|pin_required|no automatic retry|Paired: yes' docs/api.md docs/gl-x3000-verification.md`

Expected: all contract terms are present and `git diff --check` is clean.

Run: `git add docs/api.md docs/gl-x3000-verification.md && git commit -m "docs: specify interactive BLE pairing contract"`

## Task 6: Full verification, package build, and target install

**Files:**
- Modify package version/changelog only if the repository's current release workflow requires it after code is complete.

- [ ] **Step 1: Run the complete Go suite.**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 2: Build the OpenWrt packages.**

Run: `make -C package all`

Expected: all `.ipk` artifacts build with the existing gzip-tar/ustar format and aarch64 package settings.

- [ ] **Step 3: Preserve the working router bond before installation.** Capture read-only evidence and copy the router's current wattline config and BlueZ `info` file to a non-destructive `/tmp/wattline-pairing-backup-*` path on the host. Do not remove the working bond until the package is installed and the user authorizes the destructive recovery test.

- [ ] **Step 4: Transfer and install only the freshly built packages.** Use the established `cat > /tmp/name.ipk` transfer because router Dropbear SCP is unreliable. Install with `opkg`, restart wattlined, and verify the existing durable connection before testing recovery.

- [ ] **Step 5: Run the non-destructive target checks.** Verify API status, GUI asset presence, BlueZ paired/trusted/connected state, LTK presence, UCI MAC, and a protected handshake after daemon restart.

- [ ] **Step 6: With explicit authorization, run one real-BLE recovery test.** Remove only the router's BlueZ object through the GUI's clear/recover flow, click **Show pairing code**, read the Link-Power LCD, submit the code once, and capture status/log evidence. If no code appears, submit `020555` once. Do not click again after failure.

- [ ] **Step 7: Test timeout and cancel without deleting the new bond.** Start an interactive attempt on the known device only if the current bond can be safely restored; otherwise use the documented test order that preserves the new authenticated bond. Verify the agent prompt and connector are released.

- [ ] **Step 8: Run final verification before any release.** Repeat `go test ./...`, `make -C package all`, `git diff --check`, and inspect `git status --short`. Do not tag or publish a release until real-BLE verification is successful.

- [ ] **Step 9: Commit any packaging/version changes separately.** Use a focused commit message and record exact test/build output in the handoff.
