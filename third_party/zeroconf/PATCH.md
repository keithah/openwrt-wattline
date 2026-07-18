# Wattline zeroconf v1.0.0 patch

This directory is a source copy of `github.com/grandcat/zeroconf` v1.0.0 (the
version retained in the root `require` directive), with trailing whitespace in
`LICENSE` normalized and the focused `server.go` lifecycle patch below.

Upstream launches `mainloop` asynchronously and calls `shutdownEnd.Add(1)` from
inside each receive goroutine. `Shutdown` can therefore call `Wait` before an
`Add`, allowing a receive goroutine to outlive shutdown or panic by adding after
the completed wait. The local patch starts `mainloop` synchronously, increments
the WaitGroup before each receive goroutine is launched, and removes the late
increments from `recv4` and `recv6`. The probe/announcement goroutine is also
added before launch and its delays become shutdown-aware, so `Shutdown` does
not return while it can still access the responder's closed sockets.

The patch also canonicalizes host/domain suffixes after trimming their trailing
dots. Upstream compares `router.local` against `local.` and incorrectly emits
`router.local.local.`; the patched target is exactly `router.local.` and thus
matches the daemon API's `router.local` preferred-host value.

To reproduce the source copy, copy the non-test Go sources, `LICENSE`, `go.mod`,
and `go.sum` from the Go module cache for `github.com/grandcat/zeroconf@v1.0.0`,
then reapply the small `server.go` diff described above.
