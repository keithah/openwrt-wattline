# Safe RTL8761B Package Installation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make wattline package installation non-disruptive and make explicit RTL8761B activation transactional with automatic rollback.

**Architecture:** Post-install scripts only install files and defaults. A separate driverctl transaction validates artifacts, snapshots stock state, performs runtime replacement, verifies Bluetooth and wattlined health, and restores stock on any failure. Boot activation is opt-in and gated by a health marker.

**Tech Stack:** POSIX shell, OpenWrt procd/init scripts, existing package shell harnesses, Go daemon health endpoint.

---

## Task 1: Make postinst and boot hooks inert

**Files:**
- Modify `package/wattline-rtl8761b/CONTROL/postinst`
- Modify `package/wattline-rtl8761b/CONTROL/prerm`
- Modify `package/wattline-rtl8761b/etc/init.d/wattline-rtl8761b`
- Modify `package/wattlined/CONTROL/postinst`
- Test `package/tests/rtl8761b-lifecycle_test.sh`

- [ ] Add failing tests proving postinst never invokes driverctl or daemon restart and does not enable the driver init script.
- [ ] Run the tests red.
- [ ] Remove runtime activation/enable from RTL postinst and remove wattlined restart from wattlined postinst. Keep only defaults/directories and package-time checks.
- [ ] Set driver init `ENABLED=0` behavior and make `start` exit unless an explicit health marker exists.
- [ ] Run lifecycle tests green and commit.

## Task 2: Add transactional activation and rollback markers

**Files:**
- Modify `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl`
- Modify `package/wattline-rtl8761b/etc/init.d/wattline-rtl8761b`
- Test `package/tests/rtl8761b-lifecycle_test.sh`

- [ ] Add fake-root tests for each failure point: missing adapter, hash/vermagic failure, failed insmod, failed HCI up, and failed wattlined restart/health.
- [ ] Run them red.
- [ ] Add atomic `ROLLBACK_MARKER` and `HEALTH_MARKER`; write rollback before quiescing, clear it only after health succeeds, and restore all stock files/modules/service state on failure.
- [ ] Make boot start refuse activation when rollback exists or health is absent.
- [ ] Run lifecycle, artifact, and driver admission tests green; commit.

## Task 3: Add explicit activation/enable commands and documentation

**Files:**
- Modify `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl`
- Modify `package/wattline-rtl8761b/etc/init.d/wattline-rtl8761b`
- Modify `docs/gl-x3000-verification.md`
- Modify `README.md`

- [ ] Add `enable-boot` requiring a successful health marker and `disable-boot` that only disables the hook; neither changes loaded modules.
- [ ] Document staged installation: install base packages, reboot/health check, connect dongle, explicitly activate, health-check, then reboot once.
- [ ] Document recovery command that restores stock without deleting the backup.
- [ ] Run shell/doc checks and commit.

## Task 4: Verify packages offline and on target

- [ ] Run `go test ./...`, all package shell tests, `make -C package all`, `make -C package feed`, and release inventory.
- [ ] Confirm package postinst scripts are inert in a fake root.
- [ ] On a reachable GL-X3000, install only base packages first and reboot/verify reachability.
- [ ] Install the optional driver package without enabling it and reboot/verify reachability.
- [ ] With the dongle connected and a recovery path available, run explicit activation and capture rollback/health markers.
- [ ] Only after a successful activation and reboot test enable boot activation. Do not release until all checks pass.
