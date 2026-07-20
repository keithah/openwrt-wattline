# Safe RTL8761B package installation

Date: 2026-07-20
Status: approved

## Problem

The RTL package currently performs runtime module replacement from its `postinst`
and enables an early boot hook. A failed unload, `insmod`, HCI bring-up, or
daemon restart can leave the router without its normal Bluetooth stack and can
prevent a usable boot. Package installation must never require a live driver
transaction.

## Design

`wattline-rtl8761b` installation becomes file-only. Its `postinst` validates
the package architecture only and does not call `driverctl`, `insmod`,
`rmmod`, `hciconfig`, or enable an init script. The init script is disabled by
default.

`driverctl activate` remains an explicit operation. Before changing runtime it
validates kernel release, architecture, command availability, hashes, vermagic,
and supported USB presence. It writes an atomic rollback marker and preserves a
complete stock backup. Every failure path restores stock module files,
`/etc/modules.d/bluetooth`, loaded modules, HCI state, and the wattlined service.

Successful activation records a health marker only after Bluetooth readiness
and a wattlined health check pass. The boot hook may be enabled only by a
separate explicit command after a successful activation and reboot verification;
it must refuse to run when a rollback marker is present or the health marker is
absent.

`wattlined` package installation also becomes non-disruptive: its post-install
creates defaults but does not restart the daemon. A later explicit start is
allowed after the base system is confirmed reachable.

## Required tests

Shell tests run the scripts against a temporary fake root. They cover missing
adapter, bad vermagic, hash mismatch, failed `insmod`, failed HCI bring-up,
failed daemon health, rollback-marker recovery, and inert postinst behavior.
No failure case may leave packaged modules active, stock backup incomplete, or
an enabled boot hook.
