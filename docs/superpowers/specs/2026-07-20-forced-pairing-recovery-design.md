# Forced Pairing Recovery Design

Date: 2026-07-20

## Goal

Make Wattline recover from the GL-X3000 failure mode where BlueZ repeatedly
logs `hci0: security requested but not available` and the Link-Power PIN
exchange reaches its deadline. Recovery belongs entirely to Wattline. It must
remain bounded, observable in the existing pairing stepper, and must not create
a service-restart loop.

## Chosen behavior

The existing pairing recovery remains the first pass: pause the connector,
remove the stale BlueZ device/bond, rediscover the selected Link-Power, exchange
the submitted PIN, confirm the bond, trust it, reconnect, and verify the
protected Wattline handshake.

If and only if that first pass fails during the PIN/security exchange with a
timeout, Wattline performs one escalated recovery pass:

1. Report that the Bluetooth security service is being reset.
2. Restart the OpenWrt `bluetoothd` service through a narrowly scoped helper.
3. Invalidate and re-register Wattline's BlueZ pairing agent.
4. Clear the selected Link-Power's stale BlueZ device/bond again.
5. Rediscover the same MAC and retry the same user-supplied PIN once.
6. Trust, reconnect, verify the protected handshake, and persist only after
   verification succeeds.

No operation repeats the Bluetooth restart more than once. A second failure is
returned to the GUI with the complete progress timeline and an actionable
error. A new user action may start a new bounded attempt.

## Alternatives considered

### Restart Bluetooth before every recovery

This is simple but needlessly disrupts all Bluetooth activity even when
clearing the stale bond is enough. It also increases adapter churn on every
normal pairing. Rejected.

### Ask the user to restart Bluetooth manually

This avoids daemon privileges in the pairing path, but leaves the GUI unable to
fulfill its primary job and reproduces the current failure. Rejected.

### Failure-triggered, single automatic restart

This targets the observed stale-security failure while preserving the fast,
non-disruptive path. Its retry bound prevents loops and makes the operation
safe to expose as the GUI's force-recovery behavior. Chosen.

## Component boundaries

### Pairing state machine

`internal/ble.Pairing` owns retry policy and progress. It receives a recovery
callback through `PairingDeps`; it does not execute shell commands itself. The
state machine classifies only a PIN/security deadline as eligible for automatic
escalation. Authentication rejection, invalid PIN, missing device, trust
failure, persistence failure, and handshake-verification failure do not
restart Bluetooth.

### OpenWrt Bluetooth recovery helper

The daemon wiring supplies the callback. The callback invokes a packaged,
root-owned helper that restarts `/etc/init.d/bluetoothd` with a bounded command
deadline and waits for `hci0` to become available. The helper does not reload
kernel modules, alter firmware, reboot the router, or touch networking.

After a successful restart, Wattline invalidates its cached pairing-agent state
so `Prepare` registers a fresh agent against the new BlueZ process before the
second pass.

### API and GUI

The existing `POST /api/v1/pairing/request-code` request with
`{"recover":true}` remains the public trigger. No new endpoint is required.
The pairing status timeline adds stable phases for resetting Bluetooth and
retrying the PIN exchange. Both LuCI and the GL panel already render the server
timeline, so they gain the detailed recovery display without duplicating the
pairing policy in JavaScript.

## Error handling and safety

- At most two pairing passes and one `bluetoothd` restart per user request.
- Only a PIN/security timeout triggers the restart.
- The connector remains paused while BlueZ is restarted and the second bond is
  created, then resumes exactly once for protected-handshake verification.
- A temporary GUI PIN is restored after final failure and persisted only after
  the protected handshake succeeds.
- Restart/helper errors end the operation immediately and are reported through
  the pairing status without exposing tokens or PINs.
- Existing successful bonds and non-recovery pairing behavior are unchanged.

## Testing

Table-driven unit tests cover:

- normal recovery success without restarting Bluetooth;
- PIN timeout followed by one reset and a successful retry;
- second timeout stops after one reset;
- non-timeout failures never restart Bluetooth;
- agent preparation runs again after reset;
- pause/resume, PIN restoration, persistence, and progress-event ordering;
- helper timeout, missing service, and adapter-not-ready failures;
- API request and status contracts remain compatible.

On the GL-X3000, verify the known stale-security scenario: force stale BlueZ
state, press the existing recovery action, observe the reset and retry phases,
submit `020555`, and confirm `stage=paired`, `phase=complete`, `connected=true`,
and a protected handshake for `DC:04:5A:EB:72:2B`. Also confirm the router stays
reachable and no kernel module is unloaded.
