# Power-Loss Shutdown Preset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an accessible Power-loss shutdown card to LuCI and the native GL panel that safely manages the existing `no_input_shutdown` rule with a 10-minute default.

**Architecture:** Keep the generic Go rules engine and `/api/v1/rules` persistence path authoritative. Add a pure LuCI preset helper and equivalent inspectable GL helpers, then lock both implementations to one JSON fixture through behavior tests. The cards create or update only the reserved rule, preserve compatible custom fields/actions, and require confirmation before resetting an incompatible rule.

**Tech Stack:** Go rules/API tests, LuCI JavaScript modules and DOM helpers, Vue 2 render functions in the GL SDK4 bundle, Node behavior tests, OpenWrt gzip/ustar IPK packaging.

## Global Constraints

- The reserved rule name is exactly `no_input_shutdown`.
- Delay is whole minutes in the inclusive range 1–1440; default is 10.
- Runtime API `hold` is nanoseconds; one minute is `60000000000`.
- Input must be continuously absent; restored input or disconnected BLE resets the countdown.
- Shutdown runs once and requires `confirm_shutdown:true`.
- Wake is hardware-driven after input returns; the daemon never promises a software wake.
- Preserve every field and action on a compatible existing rule except GUI-owned `enabled`, `hold`, and `confirm_shutdown`.
- Never overwrite an incompatible reserved rule without explicit reset confirmation.
- Mutations are one-shot on the preferred listener and are not replayed over HTTP.
- Do not install on the router while it is unavailable; retain all target checks as NOT RUN.

---

### Task 1: Lock preset semantics and shared contract fixture

**Files:**
- Create: `package/tests/power_loss_preset.json`
- Create: `package/luci-app-wattline/www/luci-static/resources/wattline/power_loss.js`
- Create: `package/tests/power_loss_behavior_test.js`
- Modify: `internal/rules/engine_test.go`
- Modify: `package/wattlined/etc/config/wattline`

**Interfaces:**
- Consumes: `config.Rule`, `rules.Engine.Tick`, existing `/api/v1/rules` JSON shape.
- Produces: LuCI module methods `classify(rules)`, `payload(existing, enabled, minutes, reset)`, `minutes(rule)`, and `display(rule, status, telemetry)`; canonical JSON fixture used by both GUI behavior tests.

- [ ] **Step 1: Add failing engine and pure-helper tests**

Add `TestPowerLossShutdownPresetSemantics` that proves a connected input-absent snapshot fires `shutdown` only after ten continuous minutes, present input cancels/re-arms, and a disconnected tick resets the hold. Add a Node test that loads the missing LuCI module and expects:

```js
assert.deepStrictEqual(powerLoss.payload(null, true, 10, false), fixture.canonical);
assert.strictEqual(powerLoss.classify([fixture.canonical]).kind, 'compatible');
assert.strictEqual(powerLoss.classify([{ name: fixture.name, condition: 'schedule' }]).kind, 'conflict');
assert.throws(() => powerLoss.payload(null, true, 0, false), /1.*1440/);
```

- [ ] **Step 2: Verify RED**

Run:

```sh
go test ./internal/rules/ -run TestPowerLossShutdownPresetSemantics -count=1
node package/tests/power_loss_behavior_test.js
```

Expected: the Go characterization test passes against the existing engine,
while Node fails because `power_loss.js`/fixture behavior is absent. The GUI
feature remains RED.

- [ ] **Step 3: Implement the pure preset helper**

Implement the LuCI module with exact compatibility and preservation rules:

```js
function payload(existing, enabled, minutes, reset) {
	var delay = Number(minutes);
	if (!Number.isInteger(delay) || delay < 1 || delay > 1440)
		throw new Error('Delay must be a whole number from 1 to 1440 minutes');
	var rule = reset || !existing ? canonical() : Object.assign({}, existing);
	rule.name = 'no_input_shutdown';
	rule.enabled = !!enabled;
	rule.hold = delay * 60000000000;
	rule.confirm_shutdown = true;
	return rule;
}
```

`classify` returns `missing`, `compatible`, or `conflict`. Compatible means
exact name, `condition:"input_power"`, `state:"absent"`, and an actions array
containing `shutdown`. `display` treats input as present when
`telemetry.battery.status === 1 || telemetry.typec.dc_input === true`. It
combines the rule, the matching `/status.rules` entry, and telemetry into
`present`, `holding`, `fired`, or `disconnected`. Parse the engine's
`holding_for` Go-duration text (`1h2m3s`, `5m0s`, or `0s`) to compute a
non-negative approximate remaining-seconds value; never describe `last_fired`
as proof that the action succeeded.

- [ ] **Step 4: Normalize the new-install preset**

Remove the placeholder `webhook:https://ntfy.sh/CHANGME?...` action from the packaged disabled example so a newly enabled preset contains only `shutdown`. Do not migrate or delete actions from existing installations.

- [ ] **Step 5: Verify GREEN**

Run:

```sh
go test ./internal/rules/ -count=1
node package/tests/power_loss_behavior_test.js
```

Expected: PASS; the helper preserves a compatible fixture's extra webhook/action fields and rejects incompatible mutation unless `reset=true`.

- [ ] **Step 6: Commit**

```sh
git add internal/rules/engine_test.go package/tests/power_loss_preset.json \
  package/tests/power_loss_behavior_test.js \
  package/luci-app-wattline/www/luci-static/resources/wattline/power_loss.js \
  package/wattlined/etc/config/wattline
git commit -m "Add power-loss preset semantics"
```

---

### Task 2: Add the LuCI Power-loss shutdown card

**Files:**
- Modify: `package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js`
- Modify: `package/tests/luci_contract_test.sh`
- Modify: `package/tests/luci_behavior_test.js`

**Interfaces:**
- Consumes: `wattline.power_loss`, authenticated LuCI `client.json`, `/rules`, `/status`, and `/telemetry`.
- Produces: dedicated LuCI card with accessible controls, live state, compatible update, and confirmed reset.

- [ ] **Step 1: Write failing LuCI contract and behavior tests**

Assert the view requires `wattline.power_loss`, renders the exact title and hardware-wake warning, loads `/rules`, and emits `POST /rules` for missing plus `PUT /rules/no_input_shutdown` for updates. In the Node harness assert:

```js
assert.strictEqual(saveButton.disabled, true); // while one mutation is pending
assert.deepStrictEqual(lastRequest, ['PUT', '/rules/no_input_shutdown', preservedPayload]);
assert.strictEqual(confirmCalls, 1);            // incompatible reset only
assert.strictEqual(delayInput.value, '17');     // poll does not overwrite focused draft
```

- [ ] **Step 2: Verify RED**

Run:

```sh
sh package/tests/luci_contract_test.sh
node package/tests/luci_behavior_test.js
```

Expected: FAIL on the missing card/module integration.

- [ ] **Step 3: Implement state loading and one-shot save**

Extend the admin refresh load to fetch `/rules` and `/status` alongside its existing device/settings/token/pairing reads. Render through the existing generation/mutation barrier. Save via:

```js
var method = model.kind === 'missing' ? 'POST' : 'PUT';
var path = model.kind === 'missing' ? '/rules' : '/rules/no_input_shutdown';
return adminRefresh.mutation(function () {
	return api(token, port, method, path,
		wattlinePowerLoss.payload(model.rule, enabled, delayDraft, reset));
});
```

For conflict state, disable ordinary Save and show a separate Reset preset button guarded by `window.confirm`. Preserve a focused delay draft across polling. Disable both buttons during a mutation.

- [ ] **Step 4: Render accessible copy and live status**

Use labeled checkbox/number input (`min=1`, `max=1440`, `step=1`), a real button, an `aria-live="polite"` status line, and the warning:

> Shutting down Link-Power also powers off this router. It returns only when Link-Power wakes after input power comes back.

Show countdown/reset state from the pure helper. “Rule last fired” must not say shutdown succeeded.

- [ ] **Step 5: Verify and package LuCI**

Run:

```sh
node --check package/luci-app-wattline/www/luci-static/resources/wattline/power_loss.js
node --check package/luci-app-wattline/www/luci-static/resources/view/wattline/status.js
node package/tests/power_loss_behavior_test.js
node package/tests/luci_behavior_test.js
sh package/tests/luci_contract_test.sh
make -C package ipk-luci
```

Expected: all PASS and the new module is present in the LuCI IPK data archive.

- [ ] **Step 6: Commit**

```sh
git add package/luci-app-wattline package/tests/luci_contract_test.sh \
  package/tests/luci_behavior_test.js
git commit -m "Add LuCI power-loss shutdown preset"
```

---

### Task 3: Add the native GL panel card

**Files:**
- Modify: `package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js`
- Modify: `package/tests/gl_contract_test.sh`

**Interfaces:**
- Consumes: canonical fixture semantics from Task 1, GL `apiClient`, existing `adminGeneration`/`adminMutations` barrier.
- Produces: GL Vue 2 card behavior and payloads equivalent to LuCI.

- [ ] **Step 1: Add failing actual-bundle behavior tests**

Extract the new GL helper functions from the actual bundle and compare them with `power_loss_preset.json` for missing, compatible, conflict, preservation, reset, and delay bounds. Exercise component methods with deferred requests to prove:

```js
assert.deepStrictEqual(requests[0], ['POST', '/rules', fixture.canonical]);
assert.strictEqual(requests.length, 1); // pending double-click suppressed
assert.strictEqual(context.powerLossDraft.minutes, '17'); // polling preserves edit
assert.strictEqual(context.resetPrompted, true); // conflict cannot silently overwrite
```

- [ ] **Step 2: Verify RED**

Run `sh package/tests/gl_contract_test.sh`.

Expected: FAIL on missing title, routes, helpers, warning, and behavior.

- [ ] **Step 3: Implement Vue state and methods**

Add stable data fields:

```js
powerLossRule: null,
powerLossStatus: null,
powerLossDraft: { enabled: false, minutes: '10', focused: false },
powerLossBusy: false,
powerLossError: ''
```

Fetch `/rules` and `/status` inside the existing guarded admin refresh. Add `powerLossClassify`, `powerLossPayload`, `savePowerLoss(reset)`, and `powerLossDisplay`; keep their pure portions inspectable by the Node contract. Use `adminAction`/the existing mutation barrier so a stale poll cannot overwrite the result.

- [ ] **Step 4: Render the card**

Use native Vue 2 render functions with labeled checkbox and number input, real disabled buttons, `aria-live`, exact warning copy, countdown state, conflict explanation, and confirmed reset. Keep the delay draft while the input is focused and after a failed request.

- [ ] **Step 5: Verify GL behavior and gzip packaging**

Run:

```sh
sh package/tests/gl_contract_test.sh
node --check package/gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js
make -C package ipk-glapp
```

Extract the IPK and assert the decompressed `.js.gz` is byte-for-byte equal to the source bundle.

- [ ] **Step 6: Commit**

```sh
git add package/gl-app-wattline package/tests/gl_contract_test.sh
git commit -m "Add GL power-loss shutdown preset"
```

---

### Task 4: Document, verify, integrate, and push

**Files:**
- Modify: `README.md`
- Modify: `docs/api.md`
- Modify: `docs/gl-x3000-verification.md`
- Modify: `CHANGELOG.md`
- Modify: `package/tests/power_loss_behavior_test.js`

**Interfaces:**
- Consumes: completed LuCI and GL cards plus merged upstream temperature-rule/release work.
- Produces: exact operator documentation, hardware-only verification steps, final four IPKs, and pushed `main`.

- [ ] **Step 1: Add failing documentation assertions**

Require the docs to state continuous ten-minute loss, input-return cancellation, blind reset, hardware wake/router reboot, both GUI locations, reserved-rule compatibility/reset behavior, and the fact that last-fired is not proof of successful shutdown.

- [ ] **Step 2: Update documentation and defaults**

Document the preset in README and the rules section of `docs/api.md`. Add an unreleased CHANGELOG entry. Extend the target checklist with these exact NOT RUN steps:

```text
input present → remove input → countdown reaches 10m → Link-Power shuts down →
restore input → Link-Power wakes → GL-X3000 boots → wattlined reconnects →
remove input again → full countdown starts again
```

- [ ] **Step 3: Run the full verification matrix**

```sh
go test -count=1 ./...
go test -race -count=1 ./internal/state/ ./internal/ble/ ./internal/control/ \
  ./internal/auth/ ./internal/api/ ./internal/server/ ./internal/discovery/ ./internal/rules/
go vet ./...
sh package/tests/firewall-sync_test.sh
sh package/tests/provisioning_test.sh
sh package/tests/luci_contract_test.sh
node package/tests/luci_behavior_test.js
node package/tests/power_loss_behavior_test.js
sh package/tests/gl_contract_test.sh
make -C package clean all
package/check-ipk-metadata.sh package/out/*.ipk
git diff --check
```

Expected: every command passes; four version `1.3.0` IPKs exist; router checks remain NOT RUN because the GL-X3000 is unavailable.

- [ ] **Step 4: Commit documentation**

```sh
git add README.md CHANGELOG.md docs/api.md docs/gl-x3000-verification.md package/tests/power_loss_behavior_test.js
git commit -m "Document power-loss shutdown automation"
```

- [ ] **Step 5: Integrate and push**

Confirm `origin/main` is still the merge parent already incorporated by commit `664766a`. Fast-forward local `main` to the completed feature branch, rerun `go test -count=1 ./...` from the main worktree, and push without force:

```sh
git -C /home/keith/src/openwrt-wattline merge --ff-only agent/full-control-api
git -C /home/keith/src/openwrt-wattline push origin main
```

Verify `origin/main` resolves to the same commit, then remove the owned `.worktrees/full-control-api` worktree and delete the merged feature branch. Do not tag a release or attempt router installation.
