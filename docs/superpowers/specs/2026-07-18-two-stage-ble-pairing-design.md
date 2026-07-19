# Two-stage BLE pairing design

Date: 2026-07-18
Status: approved

## Problem

The current pairing API accepts a PIN before BlueZ starts pairing. The
wattlined agent immediately returns that PIN when BlueZ requests a passkey.
When Link-Power is in random-PIN mode, the configured default (`020555`) is
therefore submitted before a person can read the code on the Link-Power LCD.
The peripheral rejects the confirm value and removes the displayed code after
roughly one second.

On the GL-X3000, holding the BlueZ agent prompt open demonstrated that the
display duration is tied to the active SMP passkey exchange. A valid durable
bond is defined by all of the following, not by a transient BlueZ property:

- BlueZ reports `Paired: yes` and has a long-term key;
- the device is trusted;
- wattlined reconnects and completes the protected Link-Power handshake; and
- the device MAC and successful PIN are persisted only after that proof.

## Goals

- Make the router GUI initiate pairing before asking the user for the code.
- Keep the Link-Power code visible while the agent waits for user input.
- Submit the code into the same in-flight BlueZ pairing exchange.
- Allow `020555` to be submitted once when no code appears on the LCD.
- Stop after any failure or timeout. Never retry pairing automatically.
- Preserve the existing direct pairing endpoints for API compatibility.
- Implement the flow in both the LuCI and GL router panels; no Swift changes.
- Keep PINs out of status payloads, event history, and logs.

## Non-goals

- Extending the controller's SMP timeout beyond what BlueZ and Link-Power
  support.
- Discovering the displayed passkey over BLE; a DisplayOnly peripheral does
  not transmit its displayed value to the KeyboardOnly central.
- Erasing Link-Power's device-side bond table. Recovery can only remove this
  router's BlueZ record.
- Retrying a failed or expired exchange in the background.

## Chosen approach

Use a two-stage pairing agent backed by a bounded, concurrency-safe passkey
prompt. This keeps BlueZ's `Agent1.RequestPasskey` or `RequestPinCode` D-Bus
call pending while the HTTP API and GUI collect the displayed PIN.

This is preferred over a fixed delay, which still cannot submit a newly read
random code, and over driving `bluetoothctl` as a subprocess, which would add
brittle terminal parsing and competing BlueZ agents.

## State and data flow

1. The user scans and selects a Link-Power device.
2. **Show pairing code** starts one interactive pair operation. Recovery uses
   the same operation with `recover: true` after removing the router's stale
   BlueZ device object.
3. wattlined prepares the adapter, pauses the normal connector, rediscovers the
   selected device, and calls `org.bluez.Device1.Pair` exactly once.
4. When BlueZ calls the agent for a passkey, the agent opens a 25-second
   prompt, reports `awaiting_pin`, and blocks that D-Bus reply.
5. The GUI shows the six-digit input prefilled with `020555`, a countdown,
   **Submit code**, and **Cancel**. The user replaces the default with the code
   shown on the Link-Power LCD when one appears.
6. Submitting a valid PIN resolves the pending agent callback. The pairing
   manager continues through bond confirmation, trust, reconnect, protected
   handshake verification, and persistence.
7. A wrong PIN, timeout, D-Bus error, explicit cancellation, or failed
   reconnect ends the operation. wattlined cancels the pending prompt, resumes
   the connector, reports a terminal error, and performs no second `Pair`
   call. A new attempt requires another explicit **Show pairing code** click.

Only one scan or pair operation and only one passkey prompt may exist at a
time. PIN submission is accepted only while the selected operation is in
`awaiting_pin`.

## Internal boundaries

The BlueZ agent gains a small passkey-prompt broker with four responsibilities:

- arm the next prompt for a bounded duration;
- notify the pairing manager when BlueZ is waiting for the code;
- deliver one validated PIN to the blocked agent callback; and
- reject the callback on timeout or cancellation.

The broker contains no HTTP or BlueZ device-management logic. The pairing
manager owns lifecycle, progress, connector pause/resume, and persistence.
The existing `PairOps` continues to own BlueZ scan, pair, trust, and unpair
operations. This reuses the current pairing state machine rather than adding a
second pairing implementation.

The existing direct mode sets a known PIN before calling `Pair`; interactive
mode arms the broker instead. Both modes use the same post-pair verification
and persistence path. Every user-visible operation uses one BlueZ `Pair` call.

## HTTP API contract

All routes require the existing bearer-token authentication.

### Start an interactive exchange

`POST /api/v1/pairing/request-code`

Request:

```json
{"mac":"DC:04:5A:EB:72:2B","recover":false}
```

`recover` defaults to `false`. Success returns:

```json
{"status":"pairing"}
```

with HTTP 202. Invalid JSON or MAC returns `400 invalid_request`; a concurrent
operation returns `409 operation_in_progress`; unavailable BlueZ pairing
returns `409 capability_unsupported`.

### Submit the displayed code

`POST /api/v1/pairing/submit-pin`

Request:

```json
{"pin":"020555"}
```

The PIN must be exactly six ASCII digits. Success returns HTTP 202:

```json
{"status":"pin_submitted"}
```

Submission when no live agent prompt is waiting returns
`409 pairing_pin_not_requested`. Invalid JSON or PIN returns
`400 invalid_request`. The response never echoes the PIN.

### Cancel the exchange

`POST /api/v1/pairing/cancel`

This route requires an empty body. It rejects the pending agent request,
requests BlueZ cancellation, resumes the connector, and returns HTTP 200:

```json
{"status":"canceled"}
```

Calling it without an interactive operation returns
`409 pairing_pin_not_requested`.

### Pairing status additions

`GET /api/v1/pairing/status` retains all existing fields and adds:

```json
{
  "stage":"pairing",
  "phase":"awaiting_pin",
  "message":"Enter the code shown on Link-Power",
  "pin_required":true,
  "pin_deadline":"2026-07-19T01:23:45Z"
}
```

`pin_required` is true only while the agent callback is waiting.
`pin_deadline` is present only in that state. No PIN is returned.
After submission, the phase returns to `exchanging_pin`. Timeout and
cancellation are terminal and are visible through the existing error and event
fields.

The existing `POST /api/v1/pairing/pair` and `/recover` routes remain valid
thin compatibility paths for clients that already know a PIN. They also make
one attempt and never retry automatically.

## Router GUI behavior

Both LuCI and GL panels use the interactive routes.

- After scan, selecting a device reveals **Show pairing code** instead of an
  immediately active PIN submission form.
- While the adapter is preparing, the existing live stepper remains visible.
- At `awaiting_pin`, the card displays the PIN field, prefilled `020555`, the
  remaining prompt time, **Submit code**, and **Cancel**.
- Help text says to replace `020555` when a six-digit LCD code appears, and to
  submit the default once when no code appears.
- The detailed stepper includes **Waiting for displayed code** between
  **Locating device** and **Exchanging PIN**.
- Failure leaves the technical details visible and offers a new explicit
  **Show pairing code** attempt. It does not click, resubmit, or recover on the
  user's behalf.
- **Clear stale pairing & show code** starts the same interactive operation
  with `recover: true` after confirmation.

Buttons are disabled consistently during incompatible operations. The PIN
field remains numeric, six characters long, and accessible through an
`aria-live` status/countdown message.

## Error handling and security

- The broker rejects late, duplicate, unsolicited, and malformed submissions.
- A timeout is shorter than the underlying SMP timeout so wattlined can return
  a specific actionable error and clean up deterministically.
- The prompt is canceled on every exit path, including adapter preparation
  failure and daemon shutdown.
- PIN values are never logged, placed in progress events, or returned by
  pairing status.
- Persistence retains the existing rule: save the MAC and submitted PIN only
  after a protected reconnect succeeds.
- Authentication, request-size limits, exact-JSON decoding, and canonical API
  error envelopes remain unchanged.

## Test strategy

Development follows red-green-refactor TDD.

- Broker unit tests cover blocking until submit, exact PIN delivery, timeout,
  cancellation, duplicate submission, unsolicited submission, and one-prompt
  concurrency.
- Pairing-manager tests cover interactive lifecycle, status/deadline fields,
  persistence after verification, cleanup on every failure, and exactly one
  `Pair` call after failure.
- BlueZ helper tests prove that no retry occurs and that agent error mapping is
  stable without logging the passkey.
- API table tests cover authentication, exact JSON, MAC/PIN validation,
  success payloads, invalid state, busy state, timeout state, and the additive
  status schema for every new route.
- LuCI and GL JavaScript behavior tests cover button visibility, route bodies,
  prefilled default PIN, countdown state, submit/cancel, recovery mode, and the
  absence of automatic retries.
- `go test ./...` and `make -C package all` must pass before installation.

## On-target verification

The currently working authenticated bond must be recorded before installing a
test package. Real-BLE verification on the GL-X3000 then checks:

1. the package and daemon upgrade preserve the existing bond and reconnect;
2. after an explicitly authorized unpair/recovery test, **Show pairing code**
   leaves the LCD code visible long enough to enter;
3. the submitted code creates an authenticated long-term key;
4. the daemon trusts, reconnects, completes the protected handshake, and saves
   the MAC/PIN;
5. daemon restart reconnects without another prompt;
6. a deliberately wrong PIN causes one terminal failure and no second pairing
   attempt; and
7. timeout and cancel each release the agent prompt and resume normal
   connection behavior.

No GitHub release is created until these real-BLE checks pass.
