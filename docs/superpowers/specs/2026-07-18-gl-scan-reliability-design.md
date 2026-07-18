# GL Panel Scan Reliability Design

## Audience and outcome

This design is for Wattline maintainers. After reading it, an engineer can
implement and verify the v0.1.1 fix for GL-panel BLE scans without weakening
mutation safety or TLS support.

## Problem

The GL panel builds an HTTPS endpoint before its HTTP endpoint. Read requests
may fall back to HTTP when the router's self-signed certificate is not trusted,
but the working endpoint is not retained. Mutations are sent once to the first
endpoint, so Safari reports `Load failed` and the scan request never reaches
wattlined.

When the same scan is sent directly over authenticated HTTP, wattlined accepts
it but BlueZ may report that discovery is already in progress. This is expected
while the connector's in-flight discovery winds down. The pairing backend is
intended to share that discovery, but it recognizes only error text containing
`InProgress`; the GL-X3000 reports `Operation already in progress` instead.

## Transport design

The panel API client will remember the listener that most recently completed a
successful authenticated request. Safe GET probing keeps the existing order:
HTTPS first, then HTTP. Once a listener succeeds, later requests try that
listener first.

Mutations remain single-attempt operations. They use only the proven listener
and are never replayed after a connection error. If no listener has yet been
proven, they use the configured first listener as they do today. This preserves
HTTPS for clients that trust or pin the router certificate while allowing the
router panel to settle on HTTP when its browser rejects that certificate.

HTTP error responses still count as successful transport selection because the
listener was reached and produced an HTTP reply. Application errors continue
through the existing canonical error handling.

## BlueZ design

Discovery startup will classify BlueZ's canonical D-Bus error name
`org.bluez.Error.InProgress` as benign. A narrow compatibility fallback will
also recognize the two observed textual representations: `InProgress` and
`Operation already in progress`.

Only the discovery-in-progress condition is ignored. Other D-Bus failures
remain scan errors. When discovery is shared, the pairing scan does not stop a
session it did not start; it waits for the normal scan interval and reads the
managed BlueZ device objects.

## Tests

Regression tests will prove:

- an HTTPS network failure followed by an HTTP GET success makes the next scan
  POST use HTTP exactly once;
- a mutation is never retried after its selected listener fails;
- an HTTP application-error response still selects the reachable listener;
- BlueZ D-Bus `InProgress`, legacy text, and GL-X3000 text are benign;
- unrelated BlueZ discovery errors remain failures.

Each regression test must fail against v0.1.0 before production code changes.
The full Go and package behavior suites must pass afterward.

## Packaging and live verification

The patch release version is 0.1.1. Rebuild and install the wattlined and GL
panel packages on the GL-X3000, preserving UCI configuration, tokens, and TLS
identity. Live verification must exercise the GUI-equivalent authenticated
flow: an initial read selects a reachable listener, scan returns `202`, status
passes through `scanning` without a BlueZ error, and discovered Link-Power
advertisements appear when hardware is advertising.

## Non-goals

This change does not disable HTTPS, trust a self-signed certificate in the
browser, retry mutations, redesign the OUI RPC boundary, alter pairing PIN
semantics, or modify any Swift application.
