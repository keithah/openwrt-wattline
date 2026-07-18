# Power-Loss Shutdown Preset Design

## Goal

Let an operator configure, from either the native GL.iNet panel or LuCI, a
simple policy that shuts down Link-Power after continuous input-power loss for
a chosen delay. The default delay is ten minutes.

The GL-X3000 is powered by Link-Power. After shutdown, the daemon cannot issue
a wake command because the router is off. Recovery therefore relies on
Link-Power's hardware behavior: restored input power wakes the power station,
the router boots, and `wattlined` reconnects. The UI must state this plainly.

## Approach

The preset is a focused client of the existing generic rules API and engine. It
does not add a second automation engine or a separate persistence model. Both
GUIs manage the reserved rule name `no_input_shutdown` through
`/api/v1/rules`.

The canonical preset rule is:

```json
{
  "name": "no_input_shutdown",
  "enabled": true,
  "condition": "input_power",
  "state": "absent",
  "hold": 600000000000,
  "hysteresis_margin": 5,
  "actions": ["shutdown"],
  "confirm_shutdown": true
}
```

`hold` remains the API's Go-duration nanosecond value. The card presents it as
whole minutes. The accepted range is 1 through 1440 minutes.

## Runtime Semantics

- Input power must remain absent continuously for the configured delay.
- Input returning before expiry cancels the countdown and re-arms the rule.
- Loss of BLE telemetry resets the countdown; blind time never counts toward
  shutdown.
- At expiry, the existing serialized `shutdown` action runs once.
- After hardware wake and router reboot, the new daemon process reconnects. If
  input is present, the rule is not active. A later input loss starts a fresh
  full countdown.
- The daemon does not promise a software-driven wake. Hardware wake on restored
  input must be verified on the real Link-Power and GL-X3000.

These semantics already belong to the rules engine. The feature adds GUI
management and regression coverage rather than a parallel evaluator.

## GUI

Both the GL panel and LuCI show a **Power-loss shutdown** card with:

- an Enable switch;
- a Delay field in minutes, defaulting to 10;
- a Save button; and
- current state: input present, countdown with remaining minutes, rule last
  fired, or disconnected/countdown reset. “Last fired” does not imply that a
  shutdown command succeeded; command errors remain in daemon logs/events.

The warning reads, in substance: shutting down Link-Power also powers off this
router; it comes back only when Link-Power wakes after input power returns.

The card is accessible by keyboard, exposes labels and disabled/pending state,
and prevents duplicate saves. Polling must not overwrite a focused delay edit.

## Existing and Customized Rules

When `no_input_shutdown` is absent, Save creates the canonical preset. When it
is compatible—input absent and containing a shutdown action—the card owns only
`enabled`, `hold`, and the required shutdown confirmation. It preserves every
other existing field and action, including webhooks.

When the reserved rule exists with a different condition, state, or without a
shutdown action, the card reports **Customized rule conflict** and does not
silently overwrite it. A separate, confirmed **Reset preset** action replaces
that rule with the canonical preset using the currently entered delay.

All other rules remain untouched. LuCI's generic rules editor remains
available for advanced automation.

## API and Persistence

The panels use the existing authenticated routes:

- `GET /api/v1/rules` to load the preset;
- `POST /api/v1/rules` when the reserved rule is absent; and
- `PUT /api/v1/rules/no_input_shutdown` for compatible updates or a confirmed
  reset.

The existing save path atomically persists the complete rule set to UCI and
updates the running engine. No new endpoint or UCI key is introduced.

## Failure Handling

- A failed load leaves the previous rendered state visible with an error.
- A failed save restores the operator's draft and requires an explicit retry;
  mutations are never replayed automatically across HTTP transports.
- If the device is disconnected, configuration remains editable, but the card
  states that no countdown is running.
- Invalid delay values are rejected client-side and by tested rule conversion
  before any mutation.

## Testing

Tests cover:

- continuous absence firing once after the full delay;
- restored input cancelling and re-arming the rule;
- BLE blindness resetting the hold interval;
- canonical create and compatible update payloads;
- preservation of extra actions and unrelated rules;
- incompatible-rule conflict and confirmed reset;
- GL and LuCI rendering, validation, pending state, draft preservation, and
  exact API requests; and
- package contract/build checks.

The on-target checklist must retain the hardware-only verification: input loss,
ten-minute shutdown, Link-Power wake on restored input, router reboot, daemon
reconnect, and a second loss proving re-arm.
