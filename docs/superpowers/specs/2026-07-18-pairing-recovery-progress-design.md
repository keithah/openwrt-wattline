# Pairing Recovery and Progress Design

## Audience and outcome

This design is for Wattline maintainers and API-client authors. It defines an
honest recovery flow for an orphaned Link-Power bond after a router reflash and
the live progress information presented by the daemon, GL panel, and LuCI.

## Problem

A router reflash can erase BlueZ's long-term key while Link-Power retains its
side of the bond. The observed state is `Paired: no`, `Trusted: yes`, repeated
kernel security failures, and a protected Wattline handshake that returns `Not
paired`. The current flow may treat BlueZ's `AlreadyExists` result as pairing
success, then wait a minute for a reconnect that cannot authenticate.

The Link-Power protocol has no erase-bonds operation. `BLE_PIN` is SET-only;
its delete action returns `0xFC`, and the rejected factory opcodes provide no
bond reset. Wattline therefore cannot truthfully claim to clear Link-Power's
bond table. It can remove the router's stale BlueZ object and initiate a new
SMP PIN exchange that asks Link-Power to replace the orphaned bond.

## Recovery operation

The authenticated endpoint is:

```http
POST /api/v1/pairing/recover
Content-Type: application/json

{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}
```

It returns `202 {"status":"pairing"}` and uses the existing pairing-operation
lock and error envelope. MAC and PIN validation is identical to
`POST /api/v1/pairing/pair`. A concurrent scan, pair, recover, or unpair returns
`409 operation_in_progress`.

Recovery performs these steps in order:

1. Prepare the BlueZ agent and apply the request PIN for this attempt.
2. Pause the connector and close any active Link-Power session.
3. Call `Adapter1.RemoveDevice` unconditionally. `DoesNotExist` is success.
4. Rediscover the target's fresh advertisement and device object.
5. Call `Device1.Pair`, including the existing cancel-and-retry behavior.
6. Read `Device1.Paired`; anything other than boolean `true` is failure.
   `AlreadyExists` alone is never success.
7. Set `Device1.Trusted = true` and resume the connector.
8. Observe connector state through reconnect and protected handshake.
9. Persist `device_mac` and the PIN only after the handshake succeeds.

The ordinary pair endpoint also gains the explicit `Paired: true` check so it
cannot report a transient or false success. Recovery is the only operation
that unconditionally removes the BlueZ object first.

If recovery fails, the temporary PIN override is restored, the connector is
resumed exactly once, and no pairing values are persisted. Error text must say
which phase failed and must not tell the user to unpair another host when the
recorded state identifies an orphaned router bond.

## Status contract

`GET /api/v1/pairing/status` preserves `stage`, `error`, `target`, and
`devices`. It adds optional progress fields:

```json
{
  "stage": "pairing",
  "phase": "verifying_handshake",
  "message": "Verifying the protected Wattline handshake",
  "target": "DC:04:5A:EB:72:2B",
  "started_at": "2026-07-18T23:40:00Z",
  "updated_at": "2026-07-18T23:40:17Z",
  "elapsed_ms": 17000,
  "events": [
    {"at":"2026-07-18T23:40:00Z","phase":"preparing_adapter","message":"Preparing the Bluetooth adapter"},
    {"at":"2026-07-18T23:40:01Z","phase":"clearing_stale_bond","message":"Clearing the router's stale pairing record"}
  ],
  "devices": []
}
```

Pair phases are stable API strings:

- `preparing_adapter`
- `clearing_stale_bond` (recovery only)
- `locating_device`
- `exchanging_pin`
- `confirming_bond`
- `trusting_device`
- `reconnecting`
- `verifying_handshake`
- `saving_pairing`
- `complete`
- `failed`

Scan continues to use top-level stage `scanning`; it may report phase
`locating_device`. Existing clients that read only `stage` remain compatible.

The daemon retains only the latest operation's 32 most recent events in memory.
A new operation resets the timeline. Events use the daemon clock, are ordered
oldest to newest, and contain curated messages rather than bearer tokens, PINs,
key material, or raw D-Bus payloads. `elapsed_ms` is computed at read time while
an operation is active and freezes when it completes or fails.

## Connector progress

The recovery verifier observes the existing connection-state store. A
`connecting` state maps to `reconnecting`; `handshaking` maps to
`verifying_handshake`; `ready` plus `connected: true` is success. This reuses
the connector and protected handshake rather than creating a second BLE
session or a parallel source of truth.

The verifier times out after 60 seconds. Repeated connector states do not append
duplicate events. The final error includes the last observed connection phase
and the elapsed time.

## User interface

Both GL and LuCI pairing cards show a human-readable vertical stepper, elapsed
time, and the current message while pairing or recovery is active. Completed
steps remain checked; the active step uses a spinner; later steps remain muted;
failure marks the responsible step.

An expandable **Technical details** section renders the timestamped event list
and exact sanitized error. It is collapsed by default and uses `aria-live` only
for the current human-readable message so polling does not repeatedly announce
the entire log.

When pairing verification fails, the card presents **Clear stale pairing &
retry**. Confirmation text is explicit:

> Clear this router's saved Bluetooth pairing and request a fresh PIN bond?
> Link-Power firmware does not expose an erase-all-pairings command.

The button calls the recovery endpoint with the selected MAC and current PIN.
It is disabled during any pairing operation. The ordinary Pair button remains
available for first-time setup.

## Testing and live verification

Tests cover every phase transition, event ordering and cap, elapsed-time
freezing, secret redaction, cleanup on each failure boundary, `Paired: true`
verification, `AlreadyExists` rejection, API validation and busy policy, and
both panels' stepper/recovery behavior.

On the GL-X3000, verification must reproduce the orphaned state, run recovery,
and capture the full status timeline. Success requires BlueZ `Paired: yes` and
`Trusted: yes`, a completed protected handshake, persisted UCI pairing values,
and ordinary telemetry after reconnect. If Link-Power refuses the replacement
bond, report the exact phase and observed BlueZ state; do not claim a
device-side clear occurred.

## Non-goals

This change does not invent an undocumented Link-Power command, change the BLE
PIN on the device, erase bonds belonging to other hosts, persist diagnostic
events, transmit diagnostics off-router, or modify any Swift application.
