# Forced Pairing Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Wattline automatically reset stale GL-X3000 Bluetooth security state and retry a forced Link-Power pairing once after a PIN-exchange timeout.

**Architecture:** Keep BlueZ error classification in the BlueZ adapter, bounded retry policy in the existing `ble.Pairing` state machine, and OpenWrt service manipulation behind a packaged helper invoked through a bounded Go command. The existing recovery API remains the trigger and the existing pairing timeline carries the new reset/retry phases to both GUIs.

**Tech Stack:** Go 1.25, BlueZ D-Bus (`godbus/dbus`), OpenWrt `rc.common` shell services, existing REST pairing API, Go and POSIX-shell tests.

## Global Constraints

- At most two pairing passes and one `bluetoothd` restart per user request.
- Only a PIN/security timeout may trigger the automatic restart.
- Never unload kernel modules, replace firmware, alter networking, or reboot the router.
- Persist the MAC/PIN only after the protected Wattline handshake succeeds.
- Never include a PIN or bearer token in progress events, logs, or errors.
- Preserve all existing pairing endpoints and request shapes.
- Preserve the already-uncommitted MPTCP compatibility fix while making and committing these changes.

## File map

- `internal/ble/bluez.go`: classify the BlueZ PIN-exchange deadline with a stable sentinel.
- `internal/ble/bluez_test.go`: table tests for timeout classification.
- `internal/ble/passkey_prompt.go`: retain one submitted PIN only for the active operation and replay it once.
- `internal/ble/passkey_prompt_test.go`: prove replay is single-use and cleared at deactivation.
- `internal/ble/pairing.go`: own the bounded two-pass recovery policy and progress phases.
- `internal/ble/pairing_test.go`: exercise retry, retry limits, failure selection, and state cleanup.
- `internal/ble/security_reset.go`: bounded execution of the packaged OpenWrt recovery helper.
- `internal/ble/security_reset_test.go`: command success, failure, and timeout tests.
- `cmd/wattlined/main.go`: wire the reset callback and invalidate the cached BlueZ agent registration.
- `cmd/wattlined/pairing_agent.go`: own cached BlueZ agent registration and invalidation.
- `cmd/wattlined/pairing_agent_test.go`: verify registration caching and invalidation.
- `package/wattlined/usr/lib/wattline/restart-bluetooth-security`: restart only `bluetoothd` and wait for `hci0`.
- `package/tests/bluetooth-security-recovery_test.sh`: hermetic helper behavior tests.
- `package/Makefile`: package the helper executable and advance the IPK version.
- `docs/api.md`: document the automatic reset/retry phases and single-retry contract.

---

### Task 1: Classify the recoverable BlueZ timeout

**Files:**
- Modify: `internal/ble/bluez.go`
- Modify: `internal/ble/bluez_test.go`

**Interfaces:**
- Produces: `var ErrPairSecurityTimeout error`
- Produces: `func classifyPairCallError(error) error`
- Consumed by: Task 3 pairing retry policy.

- [ ] **Step 1: Write the failing classification tests**

Add table cases that require `context.DeadlineExceeded` and a D-Bus error containing `context deadline exceeded` to satisfy `errors.Is(got, ErrPairSecurityTimeout)`, while `org.bluez.Error.AuthenticationFailed` must not.

```go
func TestClassifyPairCallError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"context sentinel", context.DeadlineExceeded, true},
		{"dbus deadline text", errors.New("org.bluez.Error.Failed: context deadline exceeded"), true},
		{"authentication rejected", errors.New("org.bluez.Error.AuthenticationFailed"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyPairCallError(test.err)
			if errors.Is(got, ErrPairSecurityTimeout) != test.want {
				t.Fatalf("classifyPairCallError(%v) = %v", test.err, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./internal/ble -run TestClassifyPairCallError -count=1`

Expected: compile failure because `classifyPairCallError` and `ErrPairSecurityTimeout` do not exist.

- [ ] **Step 3: Implement the minimal stable classification**

```go
var ErrPairSecurityTimeout = errors.New("Bluetooth PIN security exchange timed out")

func classifyPairCallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return fmt.Errorf("%w: %v", ErrPairSecurityTimeout, err)
	}
	return err
}
```

Call `classifyPairCallError(call.Err)` in `pairOnce` before returning the D-Bus error. Preserve the existing `AlreadyExists` handling before classification.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./internal/ble -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ble/bluez.go internal/ble/bluez_test.go
git commit -m "fix(ble): classify pairing security timeouts"
```

### Task 2: Add one-shot interactive PIN replay

**Files:**
- Modify: `internal/ble/passkey_prompt.go`
- Modify: `internal/ble/passkey_prompt_test.go`

**Interfaces:**
- Produces: `func (p *PasskeyPrompt) RearmSubmitted() bool`
- Consumed by: Task 3 before the second interactive BlueZ pair pass.

- [ ] **Step 1: Write failing tests for replay and cleanup**

Test that `Submit("020555")`, the first `Wait`, and `RearmSubmitted` allow one second `Wait` to receive `020555` without another submit. Assert a second rearm returns false and `Deactivate` makes replay unavailable.

```go
func TestPasskeyPromptReplaysSubmittedPINOnce(t *testing.T) {
	p := NewPasskeyPrompt(time.Second)
	p.Activate(nil)
	if err := p.Submit("020555"); err != nil { t.Fatal(err) }
	if pin, err := p.Wait(nil); err != nil || pin != "020555" { t.Fatalf("first = %q, %v", pin, err) }
	if !p.RearmSubmitted() { t.Fatal("submitted PIN was not rearmed") }
	if pin, err := p.Wait(nil); err != nil || pin != "020555" { t.Fatalf("replay = %q, %v", pin, err) }
	if p.RearmSubmitted() { t.Fatal("PIN replayed more than once") }
	p.Deactivate()
	if p.RearmSubmitted() { t.Fatal("PIN survived deactivation") }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./internal/ble -run TestPasskeyPromptReplaysSubmittedPINOnce -count=1`

Expected: compile failure because `RearmSubmitted` does not exist.

- [ ] **Step 3: Implement bounded in-memory replay**

Add private `submittedPIN string` and `replayed bool` fields. `Submit` records the validated PIN. `Activate` clears both. `RearmSubmitted` requires an active prompt, a non-empty submitted PIN, and `replayed == false`; it creates a fresh buffered result channel containing that PIN, resets the deadline, marks `replayed`, and returns true. `Deactivate` must overwrite `submittedPIN` with `""` and mark replay unavailable.

- [ ] **Step 4: Run prompt and BLE tests**

Run: `go test ./internal/ble -run 'TestPasskeyPrompt|TestPair' -count=1`

Expected: PASS with no leaked goroutines under `go test -race ./internal/ble -run TestPasskeyPrompt -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/ble/passkey_prompt.go internal/ble/passkey_prompt_test.go
git commit -m "feat(ble): retain PIN for one recovery retry"
```

### Task 3: Implement the bounded two-pass pairing policy

**Files:**
- Modify: `internal/ble/pairing.go`
- Modify: `internal/ble/pairing_test.go`
- Modify: `internal/api/pairing_test.go`

**Interfaces:**
- Adds `PairingDeps.ResetSecurity func() error`.
- Adds phases `PhaseResettingBluetooth` and `PhaseRetryingPIN`.
- Consumes `ErrPairSecurityTimeout` and `PasskeyPrompt.RearmSubmitted()`.

- [ ] **Step 1: Add failing table tests**

Extend `fakeOps` with a scripted `pairErrs []error`, and the harness with `resets int` and `prepareCalls int`. Cover:

```go
tests := []struct {
	name       string
	errs       []error
	recover    bool
	wantResets int
	wantPairs  int
	wantStage  PairingStage
}{
	{"timeout then success", []error{ErrPairSecurityTimeout, nil}, true, 1, 2, StagePaired},
	{"timeout twice stops", []error{ErrPairSecurityTimeout, ErrPairSecurityTimeout}, true, 1, 2, StageError},
	{"auth failure no reset", []error{errors.New("authentication failed")}, true, 0, 1, StageError},
	{"ordinary pair timeout no reset", []error{ErrPairSecurityTimeout}, false, 0, 1, StageError},
}
```

Assert `Prepare` is called twice after a reset, `ResetSecurity` once at most, trust/persist only after success, pause/resume once, and event order includes reset then retry without the PIN text.

Add an API-level version of `timeout then success` that calls `POST /api/v1/pairing/request-code` with `recover:true`, submits `020555`, polls status, and asserts the terminal JSON is paired and contains both recovery phases without the PIN or bearer token.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/ble -run 'TestRecoverSecurityRetry|TestRecoverProgress' -count=1`

Expected: failures because the callback, phases, and retry do not exist.

- [ ] **Step 3: Implement a focused pairing-pass helper**

Extract the existing pair/trust portion into a closure inside `startPair`:

```go
pairPass := func() error {
	if err := p.d.Prepare(); err != nil { return err }
	if err := p.d.Ops.Pair(mac, recover, p.setPhase); err != nil { return err }
	p.setPhase(PhaseTrustingDevice, "Trusting Link-Power on this router")
	return p.d.Ops.Trust(mac)
}
```

After the first failure, retry only when all conditions hold:

```go
if recover && errors.Is(err, ErrPairSecurityTimeout) && p.d.ResetSecurity != nil {
	p.setPhase(PhaseResettingBluetooth, "Resetting stale Bluetooth security state")
	if resetErr := p.d.ResetSecurity(); resetErr != nil {
		err = fmt.Errorf("reset Bluetooth security: %w", resetErr)
	} else if !interactive || p.d.Prompt == nil || p.d.Prompt.RearmSubmitted() {
		p.setPhase(PhaseRetryingPIN, "Retrying the pairing PIN exchange")
		err = pairPass()
	} else {
		err = errors.New("the submitted pairing PIN was unavailable for retry")
	}
}
```

Keep prompt deactivation deferred until both passes finish. Preserve the existing reconnect, protected-handshake verification, persistence, and final PIN restoration paths.

- [ ] **Step 4: Run BLE and API pairing tests**

Run: `go test ./internal/ble ./internal/api -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ble/pairing.go internal/ble/pairing_test.go internal/api/pairing_test.go
git commit -m "feat(ble): retry forced pairing after security reset"
```

### Task 4: Package a narrowly scoped Bluetooth recovery helper

**Files:**
- Create: `package/wattlined/usr/lib/wattline/restart-bluetooth-security`
- Create: `package/tests/bluetooth-security-recovery_test.sh`
- Modify: `package/Makefile`

**Interfaces:**
- Produces executable `/usr/lib/wattline/restart-bluetooth-security`.
- Environment overrides for tests: `BLUETOOTH_SERVICE`, `HCICONFIG`, `SLEEP`, `READY_ATTEMPTS`.
- Consumed by: Task 5 bounded Go runner.

- [ ] **Step 1: Write the failing shell tests**

The harness supplies fake service, `hciconfig`, and sleep executables. Assert:

- the service receives exactly one `restart`;
- readiness succeeding on the third probe exits zero;
- readiness never succeeding exits non-zero after exactly `READY_ATTEMPTS` probes;
- service restart failure exits immediately;
- calls never include `insmod`, `rmmod`, `reboot`, `fw3`, or `ip`.

- [ ] **Step 2: Run the test and verify RED**

Run: `sh package/tests/bluetooth-security-recovery_test.sh`

Expected: FAIL because the helper is absent.

- [ ] **Step 3: Implement the helper**

```sh
#!/bin/sh
set -eu
BLUETOOTH_SERVICE="${BLUETOOTH_SERVICE:-/etc/init.d/bluetoothd}"
HCICONFIG="${HCICONFIG:-hciconfig}"
SLEEP="${SLEEP:-sleep}"
READY_ATTEMPTS="${READY_ATTEMPTS:-15}"

"$BLUETOOTH_SERVICE" restart
attempt=0
while [ "$attempt" -lt "$READY_ATTEMPTS" ]; do
	if "$HCICONFIG" hci0 >/dev/null 2>&1; then
		exit 0
	fi
	attempt=$((attempt + 1))
	"$SLEEP" 1
done
echo "Bluetooth adapter hci0 did not return after bluetoothd restart" >&2
exit 1
```

Update `ipk-wattlined` chmod packaging to include this helper. Set `VERSION := 0.1.5` so the router receives a real upgrade from 0.1.4.

- [ ] **Step 4: Run helper and package lifecycle tests**

Run: `sh package/tests/bluetooth-security-recovery_test.sh && sh package/tests/rtl8761b-lifecycle_test.sh && sh package/tests/provisioning_test.sh`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add package/wattlined/usr/lib/wattline/restart-bluetooth-security package/tests/bluetooth-security-recovery_test.sh package/Makefile
git commit -m "feat(package): add bounded Bluetooth security reset"
```

### Task 5: Wire bounded recovery and agent re-registration

**Files:**
- Create: `internal/ble/security_reset.go`
- Create: `internal/ble/security_reset_test.go`
- Modify: `cmd/wattlined/main.go`
- Create: `cmd/wattlined/pairing_agent.go`
- Create: `cmd/wattlined/pairing_agent_test.go`

**Interfaces:**
- Produces `func RunSecurityReset(ctx context.Context, helper string) error`.
- Produces `newPairingAgentRegistration(register func() (func(), error)) *pairingAgentRegistration` with methods `Ensure() error` and `Invalidate()`.
- Supplies `PairingDeps.ResetSecurity`.

- [ ] **Step 1: Write failing bounded-runner tests**

Use temporary executable scripts to assert successful execution, exit-status propagation, and context timeout. The error must include bounded helper output but never environment secrets.

```go
func TestRunSecurityResetTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := RunSecurityReset(ctx, writeResetScript(t, "sleep 5"))
	if !errors.Is(err, context.DeadlineExceeded) { t.Fatalf("error = %v", err) }
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/ble -run TestRunSecurityReset -count=1`

Expected: compile failure because `RunSecurityReset` does not exist.

- [ ] **Step 3: Implement the bounded runner**

Use `exec.CommandContext(ctx, helper)` with no shell, send stdout/stderr through a 4 KiB capped writer, and wrap `ctx.Err()` when the deadline expires. Set the daemon callback deadline to 25 seconds.

Move the existing cached registration into `pairingAgentRegistration`, a small mutex-protected controller. `Ensure` calls the injected register function once and stores its cancel function. `Invalidate` calls and clears the stored cancel function and marks the registration unavailable. A successful helper run calls `Invalidate()` before the state machine calls `Prepare` again. Wire:

```go
ResetSecurity: func() error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := ble.RunSecurityReset(ctx, "/usr/lib/wattline/restart-bluetooth-security"); err != nil {
		return err
	}
	agent.Invalidate()
	return nil
},
```

- [ ] **Step 4: Run daemon and BLE tests**

Run: `go test ./cmd/wattlined ./internal/ble ./internal/api -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ble/security_reset.go internal/ble/security_reset_test.go cmd/wattlined/main.go cmd/wattlined/pairing_agent.go cmd/wattlined/pairing_agent_test.go
git commit -m "feat(wattlined): recover stale BlueZ security state"
```

### Task 6: Lock the API progress contract and documentation

**Files:**
- Modify: `docs/api.md`

**Interfaces:**
- Preserves `POST /api/v1/pairing/request-code` with `recover:true`.
- Adds status phases `resetting_bluetooth` and `retrying_pin`.

- [ ] **Step 1: Confirm the API contract test from Task 3 passes**

Run: `go test ./internal/api -run TestPairingRecoverySecurityResetTimeline -count=1`

Expected: PASS; Task 3 already established the red-green cycle before retry implementation.

- [ ] **Step 2: Update `docs/api.md`**

Document the exact bounded behavior, the new phases, that Bluetooth restart affects other local Bluetooth sessions, and that only security timeout—not invalid PIN or missing device—triggers it. Retain `020555` as the documented default PIN.

- [ ] **Step 3: Run API tests and doc checks**

Run: `go test ./internal/api -count=1 && sh package/tests/luci_contract_test.sh && sh package/tests/gl_contract_test.sh`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/api.md
git commit -m "docs(api): specify forced pairing recovery"
```

### Task 7: Full verification, build, and GL-X3000 installation

**Files:**
- Verify all modified files.
- Build artifacts under `package/out/` (not committed).

**Interfaces:**
- Produces five version `0.1.5` IPKs and a matching feed index.

- [ ] **Step 1: Run all local verification**

```bash
gofmt -w internal/ble/bluez.go internal/ble/bluez_test.go \
  internal/ble/passkey_prompt.go internal/ble/passkey_prompt_test.go \
  internal/ble/pairing.go internal/ble/pairing_test.go \
  internal/ble/security_reset.go internal/ble/security_reset_test.go \
  internal/api/pairing_test.go cmd/wattlined/main.go \
  cmd/wattlined/pairing_agent.go cmd/wattlined/pairing_agent_test.go
go test ./... -count=1
for test in package/tests/*_test.sh; do
	case "$test" in *release-inventory*) continue;; esac
	sh "$test"
done
for test in package/tests/*_test.js; do node "$test"; done
git diff --check
```

Expected: all tests pass and `git diff --check` is silent.

- [ ] **Step 2: Clean-build the feed and verify inventory**

```bash
make -C package clean feed
sh package/tests/release-inventory_test.sh package/out 0.1.5
```

Expected: five 0.1.5 IPKs, `Packages`, and `Packages.gz`; metadata and inventory checks pass.

- [ ] **Step 3: Install without reloading kernel modules**

Transfer the five IPKs with SSH `cat`, install with `opkg install`, and restart only `wattlined`. The RTL package post-install is file-only; do not call `driverctl activate`, unload modules, or reboot.

- [ ] **Step 4: Verify the healthy path before forcing failure**

Assert all packages are 0.1.5, `hci0` is `UP RUNNING`, HTTP and HTTPS return authenticated 200 locally and over Tailscale, and the existing paired Link-Power remains `connected:true`.

- [ ] **Step 5: Verify forced recovery on real BLE only when explicitly requested**

Use `DC:04:5A:EB:72:2B` and PIN `020555`. Start `request-code` with `recover:true`, submit the PIN, and poll until terminal. Confirm the status includes bond clearing and either completes without reset or, when stale security is reproduced, includes `resetting_bluetooth` and `retrying_pin`, then reaches `stage:"paired"`, `phase:"complete"`, and `connected:true`. Confirm `hciconfig hci0` remains healthy and no kernel module was unloaded.

- [ ] **Step 6: Commit any final test-only adjustments and report evidence**

Do not push or publish a GitHub release unless the user separately requests it. Report local tests, build output, installed versions, API timings, pairing timeline, and any real-BLE scenario that could not safely be reproduced.
