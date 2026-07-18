# Optional RTL8761B Driver Package Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship and validate an optional, reversible `wattline-rtl8761b` IPK that installs the proven RTL8761B modules and firmware only on GL-X3000 Linux 5.4.211 targets, then publish all five `v0.1.0` release packages.

**Architecture:** Keep `wattline-bt` as the generic BlueZ/base-kmod dependency and add a fifth kernel-specific package. The optional package owns immutable patched artifacts in a private directory and uses one transactional shell controller to back up, activate, verify, repair, and restore GL's stock module paths. Init and USB-hotplug adapters call that controller; host tests inject a fake filesystem and commands to prove admission, rollback, idempotence, and removal before the live-router sequence.

**Tech Stack:** Go 1.22, POSIX/BusyBox shell, OpenWrt opkg/procd/hotplug, Linux 5.4.211 AArch64 kernel modules, BlueZ 5.64, GNU tar/gzip IPK packaging, GitHub Actions.

## Global Constraints

- The optional package name is `wattline-rtl8761b`; release version is exactly `0.1.0`.
- Hard-require `uname -r == 5.4.211` and module vermagic `5.4.211 SMP mod_unload aarch64` before modifying active module files.
- Supported USB IDs are exactly `2357:0604` and `0bda:8771`; unrelated adapters retain GL's stock stack.
- Bundle the verified modules and canonical `rtl8761bu_fw.bin`/`rtl8761bu_config.bin`; never download code or firmware during router installation.
- Preserve `wattline-bt` as the generic package; neither `wattline-bt` nor `wattlined` depends on `wattline-rtl8761b`.
- Do not declare payload files that conflict with `kmod-bluetooth` ownership; activate private packaged copies transactionally.
- Back up stock modules/configuration once, roll back failed activation, and restore stock state on removal.
- A firmware/kernel upgrade away from 5.4.211 must fail closed and requires a new module build/package.
- Keep all Swift code out of scope.
- Do not create or push `v0.1.0` until host CI, current-router install/remove/reinstall, and reboot validation all pass.

---

## File Structure

### New package inputs

- `package/wattline-rtl8761b/CONTROL/control` — package identity, architecture, and dependency on `wattline-bt`.
- `package/wattline-rtl8761b/CONTROL/preinst` — target kernel/architecture admission before unpack mutation.
- `package/wattline-rtl8761b/CONTROL/postinst` — activate, enable init service, and restart dependent services.
- `package/wattline-rtl8761b/CONTROL/prerm` — restore the stock stack before removal.
- `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl` — sole owner of detect/admit/activate/status/restore behavior.
- `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/modules/5.4.211/{btintel,btrtl,btusb}.ko` — immutable known-good private copies.
- `package/wattline-rtl8761b/lib/firmware/rtl_bt/{rtl8761bu_fw.bin,rtl8761bu_config.bin}` — packaged canonical firmware.
- `package/wattline-rtl8761b/etc/init.d/wattline-rtl8761b` — boot repair/load adapter, ordered before `wattlined`.
- `package/wattline-rtl8761b/etc/hotplug.d/usb/20-wattline-rtl8761b` — supported-ID attach adapter.
- `package/wattline-rtl8761b/usr/share/wattline-rtl8761b/{SHA256SUMS,PROVENANCE.md,COPYING,WHENCE,LICENSE.rtlwifi_firmware.txt,linux-5.4.211-rtl8761b-gl-abi.patch,router-4.8.3.config}` — integrity, licensing, and reproducibility record.

### New tests

- `package/tests/rtl8761b-artifacts_test.sh` — committed hashes, ELF architecture, vermagic, aliases, firmware, patch, and license provenance.
- `package/tests/rtl8761b-driver_test.sh` — fake-root admission, activation, rollback, repair, and restore behavior.
- `package/tests/rtl8761b-lifecycle_test.sh` — CONTROL/init/hotplug call order and ID filtering.
- `package/tests/release-inventory_test.sh` — exactly five version-matched IPKs and feed records.

### Modified files

- `internal/server/secure_paths_unix.go` and `internal/server/secure_paths_unix_test.go` — GL `root:root 0775` TLS-parent compatibility without relaxing world/non-root-group protection.
- `package/Makefile` and `package/check-ipk-metadata.sh` — build/check the fifth IPK.
- `.github/workflows/ci.yml` and `.github/workflows/release.yml` — run package shell tests and publish the full inventory.
- `README.md`, `CHANGELOG.md`, `dongle-rtl8761b/README.md`, and `docs/gl-x3000-verification.md` — package selection, recovery, exact kernel boundary, and evidence checklist.

---

### Task 1: Finish the GL TLS-parent compatibility fix

**Files:**
- Modify: `internal/server/secure_paths_unix.go`
- Create: `internal/server/secure_paths_unix_test.go`

**Interfaces:**
- Produces: `writableByUntrusted(mode os.FileMode, gid uint32) bool`, used only by trusted TLS ancestry validation.
- Preserves: rejection of world-write, non-root-group write, symlink components, untrusted owners, and unsafe sticky ancestry.

- [ ] **Step 1: Confirm the existing RED evidence and current diff**

Run:

```bash
git diff -- internal/server/secure_paths_unix.go internal/server/secure_paths_unix_test.go
```

Expected: a table covering root-group `0775`, non-root-group `0775`, root-group `0777`, and ordinary `0755`, plus the minimal helper implementation. The earlier RED output must be retained in the handoff: `undefined: writableByUntrusted`.

- [ ] **Step 2: Verify focused security behavior**

Run:

```bash
go test -count=1 ./internal/server/ -run 'TestWritableByUntrustedDirectoryPolicy|TestCertificateRejectsUnsafeGrandparent|TestCertificateRejectsSymlinksUnsafeParentsAndAliases' -v
```

Expected: every table row and prior unsafe-parent test passes.

- [ ] **Step 3: Verify initialization through a root-owned group-writable ancestor**

Add a subprocess/root-capable integration row when running as UID 0; otherwise keep the pure policy table authoritative:

```go
if os.Geteuid() == 0 {
    base := t.TempDir()
    parent := filepath.Join(base, "etc", "wattline", "tls")
    if err := os.MkdirAll(parent, 0o700); err != nil { t.Fatal(err) }
    if err := os.Chown(filepath.Join(base, "etc"), 0, 0); err != nil { t.Fatal(err) }
    if err := os.Chmod(filepath.Join(base, "etc"), 0o775); err != nil { t.Fatal(err) }
    _, err := EnsureCertificate(filepath.Join(parent, "server.crt"), filepath.Join(parent, "server.key"), nil)
    if err != nil { t.Fatal(err) }
}
```

- [ ] **Step 4: Run the full Go security suite**

Run:

```bash
go test -count=1 ./internal/server/ ./cmd/wattlined/
go test -race -count=1 ./internal/server/ ./cmd/wattlined/
```

Expected: PASS with zero races.

- [ ] **Step 5: Commit**

```bash
git add internal/server/secure_paths_unix.go internal/server/secure_paths_unix_test.go
git commit -m "Allow trusted GL root-group TLS ancestors"
```

---

### Task 2: Vendor and prove the known-good driver artifacts

**Files:**
- Create: `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/modules/5.4.211/{btintel,btrtl,btusb}.ko`
- Create: `package/wattline-rtl8761b/lib/firmware/rtl_bt/{rtl8761bu_fw.bin,rtl8761bu_config.bin}`
- Create: `package/wattline-rtl8761b/usr/share/wattline-rtl8761b/{SHA256SUMS,PROVENANCE.md,COPYING,WHENCE,LICENSE.rtlwifi_firmware.txt,linux-5.4.211-rtl8761b-gl-abi.patch,router-4.8.3.config}`
- Create: `package/tests/rtl8761b-artifacts_test.sh`

**Interfaces:**
- Produces immutable `PAYLOAD_DIR/modules/5.4.211/*.ko`, firmware, and `SHA256SUMS` consumed by `driverctl verify`.
- Exact known-good hashes:
  - `btusb.ko`: `82a811239f2530aedc2fa9eca79276b517241b9a204f42b84d26af9c3a8e41c1`
  - `btrtl.ko`: `ef80e6fdcb5affe3556db6933a216babe2b96a66cf8aae00ee467d5d13f3dbb6`
  - `btintel.ko`: `4237ce29b705e9b1d00d7cba064ba5d01bec89756f7a98176ecf029390fa2bf9`
  - `rtl8761bu_fw.bin`: `1d7a9597349ad89344fa16c1913d3e39e9a12e966e417ca16871bc79bbe59edb`
  - `rtl8761bu_config.bin`: `6c28a3f07c6a30ed208c4b64862a23f02b7d93543ea980edd24df16bab45095f`

- [ ] **Step 1: Write the failing artifact contract test**

Create a POSIX shell test that asserts all files exist, hashes match the constants above, `modinfo` reports the exact vermagic, and `btusb.ko` exposes both USB aliases:

```sh
#!/bin/sh
set -eu
ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
BASE="$ROOT/package/wattline-rtl8761b"
MOD="$BASE/usr/lib/wattline/rtl8761b/modules/5.4.211"
FW="$BASE/lib/firmware/rtl_bt"

printf '%s  %s\n' '82a811239f2530aedc2fa9eca79276b517241b9a204f42b84d26af9c3a8e41c1' "$MOD/btusb.ko" | sha256sum -c -
printf '%s  %s\n' 'ef80e6fdcb5affe3556db6933a216babe2b96a66cf8aae00ee467d5d13f3dbb6' "$MOD/btrtl.ko" | sha256sum -c -
printf '%s  %s\n' '4237ce29b705e9b1d00d7cba064ba5d01bec89756f7a98176ecf029390fa2bf9' "$MOD/btintel.ko" | sha256sum -c -
printf '%s  %s\n' '1d7a9597349ad89344fa16c1913d3e39e9a12e966e417ca16871bc79bbe59edb' "$FW/rtl8761bu_fw.bin" | sha256sum -c -
printf '%s  %s\n' '6c28a3f07c6a30ed208c4b64862a23f02b7d93543ea980edd24df16bab45095f' "$FW/rtl8761bu_config.bin" | sha256sum -c -
[ "$(modinfo -F vermagic "$MOD/btusb.ko")" = '5.4.211 SMP mod_unload aarch64' ]
modinfo "$MOD/btusb.ko" | grep -Fqi 'usb:v2357p0604'
modinfo "$MOD/btusb.ko" | grep -Fqi 'usb:v0BDAp8771'
for name in SHA256SUMS PROVENANCE.md COPYING WHENCE LICENSE.rtlwifi_firmware.txt linux-5.4.211-rtl8761b-gl-abi.patch router-4.8.3.config; do
    test -s "$BASE/usr/share/wattline-rtl8761b/$name"
done
```

- [ ] **Step 2: Run the test to verify RED**

Run: `sh package/tests/rtl8761b-artifacts_test.sh`

Expected: FAIL because the optional package artifacts do not exist.

- [ ] **Step 3: Copy only the verified binaries and generate provenance**

Use the known build tree as the source, then independently verify hashes before staging:

```bash
mkdir -p package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/modules/5.4.211
mkdir -p package/wattline-rtl8761b/lib/firmware/rtl_bt
mkdir -p package/wattline-rtl8761b/usr/share/wattline-rtl8761b
cp /home/keith/src/rtl8761b-build/linux-5.4.211/drivers/bluetooth/{btintel,btrtl,btusb}.ko \
  package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/modules/5.4.211/
cp /home/keith/src/rtl8761b-build/fw-canonical/rtl8761bu_{fw,config}.bin \
  package/wattline-rtl8761b/lib/firmware/rtl_bt/
cp /home/keith/src/rtl8761b-build/router-483.config \
  package/wattline-rtl8761b/usr/share/wattline-rtl8761b/router-4.8.3.config
```

Generate the patch by diffing the pristine Linux 5.4.211 tarball against only `drivers/bluetooth/btrtl.c`, `drivers/bluetooth/btusb.c`, and `include/linux/timer.h`; do not include build output. Record source URL/version, cross-compiler, config origin, GL timer ABI padding, module hashes, firmware upstream commit/paths, and reproduction commands in `PROVENANCE.md`. Copy Linux `COPYING` and the authoritative linux-firmware WHENCE/license files supporting the bundled blobs.

- [ ] **Step 4: Write the package-relative manifest**

From the package data root:

```bash
cd package/wattline-rtl8761b
sha256sum \
  usr/lib/wattline/rtl8761b/modules/5.4.211/btintel.ko \
  usr/lib/wattline/rtl8761b/modules/5.4.211/btrtl.ko \
  usr/lib/wattline/rtl8761b/modules/5.4.211/btusb.ko \
  lib/firmware/rtl_bt/rtl8761bu_fw.bin \
  lib/firmware/rtl_bt/rtl8761bu_config.bin \
  > usr/share/wattline-rtl8761b/SHA256SUMS
```

- [ ] **Step 5: Verify GREEN and commit**

Run:

```bash
sh package/tests/rtl8761b-artifacts_test.sh
git diff --check
```

Expected: PASS and clean diff check.

```bash
git add package/wattline-rtl8761b package/tests/rtl8761b-artifacts_test.sh
git commit -m "Vendor verified RTL8761B driver artifacts"
```

---

### Task 3: Implement transactional driver admission, activation, and restoration

**Files:**
- Create: `package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl`
- Create: `package/tests/rtl8761b-driver_test.sh`

**Interfaces:**
- Command: `driverctl detect` — exit 0 and print the matched lowercase USB ID, or exit 1 with no output.
- Command: `driverctl admit` — verify kernel, architecture, tools, manifest, and vermagic without mutation.
- Command: `driverctl activate [--require-device]` — transactional backup/copy/load; optional strict adapter/HCI requirement.
- Command: `driverctl status` — report `stock`, `packaged`, `drifted`, or `unsupported` without mutation.
- Command: `driverctl restore` — restore original modules and `/etc/modules.d/bluetooth`; retain backups on failure.
- Test injection variables: `ROOT_PREFIX`, `PAYLOAD_DIR`, `STATE_DIR`, `LOCK_FILE`, `KERNEL_RELEASE`, `MACHINE`, `PROC_MODULES`, `SYS_USB`, `WATTLINE_SERVICE`, and command `PATH`.

- [ ] **Step 1: Write failing table scenarios in the shell harness**

The harness must create a fake root, stock module files, module list, USB sysfs entries, and fake commands that append to `CALLS`. Cover these exact scenarios:

```text
admit: kernel 5.4.211 + aarch64 + valid artifacts -> success, no mutation
admit: kernel 6.6.0 -> failure, no backup/temp/active mutation
detect: 2357:0604 -> match
detect: 0bda:8771 -> match
detect: 0a12:0001 -> no match
activate: backup once, atomic copies, module list removes only btusb, load btintel/btrtl/btusb, hci up
activate twice: original backup hashes unchanged
activate after simulated stock overwrite: active packaged hashes repaired
activate failure at each copy/load boundary: original active files and module list restored
restore: original hashes/config returned and packaged-only btrtl removed
restore failure: backup remains intact and command exits nonzero
```

Use an injected `FAIL_AT` in fake `cp`, `mv`, `rmmod`, or `insmod` commands to make rollback boundaries deterministic.

- [ ] **Step 2: Run the harness to verify RED**

Run: `sh package/tests/rtl8761b-driver_test.sh`

Expected: FAIL because `driverctl` does not exist.

- [ ] **Step 3: Implement admission and detection only**

Start `driverctl` with strict defaults and the exact supported-ID predicate:

```sh
#!/bin/sh
set -eu
EXPECTED_KERNEL=5.4.211
EXPECTED_MACHINE=aarch64
ROOT_PREFIX="${ROOT_PREFIX:-}"
PAYLOAD_DIR="${PAYLOAD_DIR:-$ROOT_PREFIX/usr/lib/wattline/rtl8761b}"
STATE_DIR="${STATE_DIR:-$ROOT_PREFIX/etc/wattline/rtl8761b-stock}"
LOCK_FILE="${LOCK_FILE:-$ROOT_PREFIX/var/lock/wattline-rtl8761b.lock}"
SYS_USB="${SYS_USB:-$ROOT_PREFIX/sys/bus/usb/devices}"
KERNEL_RELEASE="${KERNEL_RELEASE:-$(uname -r)}"
MACHINE="${MACHINE:-$(uname -m)}"

supported_id() {
    case "$(printf '%s' "$1" | tr 'A-F' 'a-f')" in
        2357:0604|0bda:8771) return 0 ;;
        *) return 1 ;;
    esac
}
```

`admit` must check every Global Constraint before acquiring the mutation lock. `detect` must read sysfs files rather than parse `lsusb` output.

- [ ] **Step 4: Run admission/detection rows GREEN**

Run: `sh package/tests/rtl8761b-driver_test.sh admit detect`

Expected: PASS for the admission and detection subset.

- [ ] **Step 5: Implement backup and atomic activation**

Use `flock` and a same-directory copy/rename primitive:

```sh
atomic_install() {
    source=$1 destination=$2
    temporary="${destination}.wattline-new.$$"
    cp "$source" "$temporary"
    chmod 0644 "$temporary"
    expected="$(sha256sum "$source" | awk '{print $1}')"
    actual="$(sha256sum "$temporary" | awk '{print $1}')"
    [ "$actual" = "$expected" ] || { rm -f "$temporary"; return 1; }
    mv -f "$temporary" "$destination"
}
```

Create the stock backup in `STATE_DIR.new.$$`, including absent markers and module-list metadata, then atomically rename it to `STATE_DIR` only when complete. Never replace an existing complete backup. Install a trap after the first active mutation so any error invokes the internal restore routine before returning nonzero.

- [ ] **Step 6: Implement ordered load, status, repair, and restore**

The ordered call log must be:

```text
wattlined stop
hciconfig hci0 down (when present)
rmmod btusb
rmmod btrtl
rmmod btintel
insmod .../btintel.ko
insmod .../btrtl.ko
insmod .../btusb.ko
hciconfig hci0 up (when present; mandatory with --require-device)
wattlined restart
```

Treat “module not loaded” during `rmmod` as nonfatal, but treat any `insmod` failure as activation failure. `status` compares private, active, backup, and configuration hashes without writing. `restore` uses the same atomic primitive, restores absent markers by removal, reloads stock `btintel`/`btusb`, and deletes `STATE_DIR` only after success.

- [ ] **Step 7: Run the complete RED/GREEN harness and shell syntax check**

Run:

```bash
sh -n package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl
sh package/tests/rtl8761b-driver_test.sh
```

Expected: all scenarios PASS; fake call order matches exactly; no `.wattline-new.*` or incomplete backup remains.

- [ ] **Step 8: Commit**

```bash
git add package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl package/tests/rtl8761b-driver_test.sh
git commit -m "Add transactional RTL8761B driver control"
```

---

### Task 4: Add package lifecycle, boot, and hotplug integration

**Files:**
- Create: `package/wattline-rtl8761b/CONTROL/{control,preinst,postinst,prerm}`
- Create: `package/wattline-rtl8761b/etc/init.d/wattline-rtl8761b`
- Create: `package/wattline-rtl8761b/etc/hotplug.d/usb/20-wattline-rtl8761b`
- Create: `package/tests/rtl8761b-lifecycle_test.sh`

**Interfaces:**
- Package metadata: `Package: wattline-rtl8761b`, `Version: 0.1.0`, `Architecture: aarch64_cortex-a53`, `Depends: wattline-bt`.
- Init service: `START=15`, calls `driverctl activate` and runs before `wattlined` (`START=95`).
- Hotplug: only `ACTION=add` and supported `PRODUCT` vendor/product pairs call `driverctl activate --require-device`.

- [ ] **Step 1: Write the failing lifecycle harness**

Inject a fake `driverctl` that logs arguments and assert:

```text
preinst exact kernel -> 0
preinst 6.6.0 -> nonzero and no driverctl call
postinst live root -> admit, activate --require-device, enable
postinst IPKG_INSTROOT set -> no live mutation
prerm live root -> restore
init start -> activate
USB add 2357/0604/* -> activate --require-device
USB add 0bda/8771/* -> activate --require-device
USB remove or unrelated add -> no call
```

- [ ] **Step 2: Run to verify RED**

Run: `sh package/tests/rtl8761b-lifecycle_test.sh`

Expected: FAIL because package lifecycle files do not exist.

- [ ] **Step 3: Implement minimal CONTROL scripts and adapters**

`preinst` must duplicate only the immutable admission facts needed before data unpack:

```sh
#!/bin/sh
set -eu
[ -n "${IPKG_INSTROOT:-}" ] && exit 0
[ "$(uname -r)" = 5.4.211 ] || { echo 'wattline-rtl8761b requires Linux 5.4.211' >&2; exit 1; }
[ "$(uname -m)" = aarch64 ] || { echo 'wattline-rtl8761b requires aarch64' >&2; exit 1; }
exit 0
```

`postinst` calls the newly installed controller, enables `/etc/init.d/wattline-rtl8761b`, and propagates failures. `prerm` calls `restore` before opkg deletes package-owned files. The hotplug adapter normalizes `PRODUCT` to four-digit lowercase vendor/product strings before matching.

- [ ] **Step 4: Verify lifecycle GREEN**

Run:

```bash
sh package/tests/rtl8761b-lifecycle_test.sh
sh -n package/wattline-rtl8761b/CONTROL/preinst
sh -n package/wattline-rtl8761b/CONTROL/postinst
sh -n package/wattline-rtl8761b/CONTROL/prerm
sh -n package/wattline-rtl8761b/usr/lib/wattline/rtl8761b/driverctl
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add package/wattline-rtl8761b/CONTROL package/wattline-rtl8761b/etc package/tests/rtl8761b-lifecycle_test.sh
git commit -m "Package RTL8761B boot and hotplug lifecycle"
```

---

### Task 5: Build, inspect, and document five IPKs

**Files:**
- Modify: `package/Makefile`
- Modify: `package/check-ipk-metadata.sh`
- Create: `package/tests/release-inventory_test.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `dongle-rtl8761b/README.md`
- Modify: `docs/gl-x3000-verification.md`

**Interfaces:**
- `make -C package all` produces exactly five IPKs, including `wattline-rtl8761b_VERSION_aarch64_cortex-a53.ipk`.
- `make -C package feed` produces exactly five `Packages` records.
- CI runs the explicit host behavior/contract shell tests and existing Node
  tests, builds the packages, then runs the release inventory test.

- [ ] **Step 1: Write the failing release inventory test**

The test takes `package/out` and a version, then asserts exact basenames and feed records:

```sh
expected="
gl-app-wattline_${VERSION}_all.ipk
luci-app-wattline_${VERSION}_all.ipk
wattline-bt_${VERSION}_all.ipk
wattline-rtl8761b_${VERSION}_aarch64_cortex-a53.ipk
wattlined_${VERSION}_aarch64_cortex-a53.ipk"
actual="$(find "$OUT" -maxdepth 1 -type f -name '*.ipk' -exec basename {} \; | sort)"
[ "$actual" = "$(printf '%s\n' "$expected" | sed '/^$/d' | sort)" ]
[ "$(grep -c '^Package:' "$OUT/Packages")" -eq 5 ]
```

- [ ] **Step 2: Verify RED**

Run: `make -C package clean feed && sh package/tests/release-inventory_test.sh package/out 0.1.0`

Expected: FAIL because only four IPKs exist.

- [ ] **Step 3: Add the fifth Makefile target and metadata policy**

Add `ipk-rtl8761b` to `.PHONY` and `all`, stage the new tree through the existing `make_ipk` macro, and explicitly set executable modes for CONTROL scripts, init, hotplug, and `driverctl`. Extend `check-ipk-metadata.sh` to assert:

```text
CONTROL/preinst, postinst, prerm are 0755
driverctl, init, hotplug are 0755
module files and firmware are 0644
all required provenance files exist
no private keys/tokens/backups are shipped
control Version equals filename version
```

- [ ] **Step 4: Update CI/release workflow and docs**

CI must execute:

```yaml
- name: Package behavior tests
  run: |
    set -eu
    for test in \
      package/tests/firewall-sync_test.sh \
      package/tests/provisioning_test.sh \
      package/tests/luci_contract_test.sh \
      package/tests/gl_contract_test.sh \
      package/tests/rtl8761b-artifacts_test.sh \
      package/tests/rtl8761b-driver_test.sh \
      package/tests/rtl8761b-lifecycle_test.sh; do
        sh "$test"
    done
    node package/tests/luci_behavior_test.js
    node package/tests/power_loss_behavior_test.js
- name: Build packages (ipks + metadata check)
  run: make -C package feed
- name: Release inventory
  run: sh package/tests/release-inventory_test.sh package/out 0.1.0
```

Keep release file globbing `package/out/*.ipk`, but update human text from four to five packages and document the sysfs selection command. State that `wattline-rtl8761b` is installed only for `2357:0604`/`0bda:8771`, bundles firmware, hard-fails off 5.4.211, restores stock on removal, and must be rebuilt after a kernel change.

- [ ] **Step 5: Build and verify GREEN**

Run:

```bash
make -C package clean feed
sh package/tests/release-inventory_test.sh package/out 0.1.0
TAR="$(command -v gtar || command -v gnutar || echo tar)" package/check-ipk-metadata.sh package/out/*.ipk
```

Expected: five IPKs, five feed records, all metadata checks PASS.

- [ ] **Step 6: Commit**

```bash
git add package/Makefile package/check-ipk-metadata.sh package/tests/release-inventory_test.sh \
  .github/workflows/ci.yml .github/workflows/release.yml README.md CHANGELOG.md \
  dongle-rtl8761b/README.md docs/gl-x3000-verification.md
git commit -m "Build optional RTL8761B release package"
```

---

### Task 6: Run the complete host verification gate

**Files:**
- No production changes expected.

**Interfaces:**
- Consumes all prior tasks.
- Produces the exact commit and artifact hashes eligible for router deployment.

- [ ] **Step 1: Run all Go checks**

```bash
go test -count=1 ./...
go test -race -count=1 ./internal/state/ ./internal/ble/ ./internal/control/ \
  ./internal/auth/ ./internal/api/ ./internal/server/ ./internal/discovery/ ./cmd/wattlined/
go vet ./...
```

Expected: PASS, zero races, zero vet diagnostics.

- [ ] **Step 2: Run all package/UI checks**

```bash
set -eu
for test in \
  package/tests/firewall-sync_test.sh \
  package/tests/provisioning_test.sh \
  package/tests/luci_contract_test.sh \
  package/tests/gl_contract_test.sh \
  package/tests/rtl8761b-artifacts_test.sh \
  package/tests/rtl8761b-driver_test.sh \
  package/tests/rtl8761b-lifecycle_test.sh; do
    sh "$test"
done
node package/tests/luci_behavior_test.js
node package/tests/power_loss_behavior_test.js
make -C package clean feed
sh package/tests/release-inventory_test.sh package/out 0.1.0
git diff --check
```

Expected: PASS; exactly five IPKs and five feed records.

- [ ] **Step 3: Record immutable deploy hashes**

```bash
sha256sum package/out/*.ipk package/out/Packages package/out/Packages.gz | tee /tmp/wattline-0.1.0.SHA256SUMS
git status --short
git rev-parse HEAD
```

Expected: clean worktree; manifest contains seven records.

---

### Task 7: Push main, require CI, and validate the package lifecycle on GL-X3000

**Files:**
- Router state at `root@100.87.232.42` only.

**Interfaces:**
- Router SSH uses the isolated known-hosts file `/tmp/wattline-router-known-hosts`.
- The router must report kernel `5.4.211` and USB `2357:0604` before optional package installation.

- [ ] **Step 1: Push the verified commits and require main CI**

```bash
git push origin main
head_sha="$(git rev-parse HEAD)"
run_id="$(gh run list -R keithah/openwrt-wattline --branch main --limit 10 \
  --json databaseId,headSha --jq 'map(select(.headSha == "'"$head_sha"'"))[0].databaseId')"
test -n "$run_id"
gh run watch "$run_id" -R keithah/openwrt-wattline --exit-status
```

Expected: pushed HEAD CI completes successfully, including all five-package checks. Use the GitHub workflow monitor helper if it becomes available; otherwise record the documented `gh` fallback.

- [ ] **Step 2: Capture the pre-install recovery baseline**

Over SSH, record:

```sh
uname -r
for d in /sys/bus/usb/devices/*; do
  [ -r "$d/idVendor" ] || continue
  printf '%s:' "$(cat "$d/idVendor")"; cat "$d/idProduct"
done
sha256sum /lib/modules/5.4.211/btintel.ko /lib/modules/5.4.211/btusb.ko
cat /etc/modules.d/bluetooth
opkg status wattline-bt wattlined luci-app-wattline gl-app-wattline
```

Expected: kernel exact, supported USB ID present, stock hashes/config recorded.

- [ ] **Step 3: Stream and verify the exact five artifacts**

Use SSH `cat` because Dropbear scp is unreliable, then compare remote hashes to `/tmp/wattline-0.1.0.SHA256SUMS`. Any mismatch stops deployment.

- [ ] **Step 4: Install all generic packages plus the selected optional package**

```sh
opkg install --force-reinstall \
  /tmp/wattline-bt_0.1.0_all.ipk \
  /tmp/wattline-rtl8761b_0.1.0_aarch64_cortex-a53.ipk \
  /tmp/wattlined_0.1.0_aarch64_cortex-a53.ipk \
  /tmp/luci-app-wattline_0.1.0_all.ipk \
  /tmp/gl-app-wattline_0.1.0_all.ipk
```

Expected: every package status is `install ok installed`; TLS initialization creates a certificate/token without the prior `/etc` error; driver activation returns zero.

- [ ] **Step 5: Verify active package-driven Bluetooth and daemon state**

Record:

```sh
/usr/lib/wattline/rtl8761b/driverctl status
sha256sum /lib/modules/5.4.211/{btintel,btrtl,btusb}.ko
lsmod | grep -E '^(btintel|btrtl|btusb|bluetooth) '
dmesg | grep -Ei 'rtl|8761|firmware' | tail -n 50
hciconfig -a
timeout 20 hcitool lescan --duplicates | sed -n '1,20p'
/etc/init.d/wattlined status
logread -e wattline | tail -n 100
```

Expected: packaged hashes, all modules loaded, firmware version/upload logged, `hci0 UP RUNNING`, nonempty BLE advertisements, active daemon. If Link-Power is available, `/api/v1/device` eventually reports connected; absence of the power bank does not invalidate driver verification.

- [ ] **Step 6: Prove uninstall restoration and reinstall**

Remove only `wattline-rtl8761b`, then compare stock hashes and `/etc/modules.d/bluetooth` byte-for-byte to Step 2. Reinstall the exact same IPK and repeat Step 5. Do not proceed if backup/restoration differs.

- [ ] **Step 7: Reboot and prove persistence**

Reboot the router, wait for SSH to return in bounded polls, then repeat package status, active hashes, `lsmod`, firmware log, `hciconfig`, BLE scan, daemon status, HTTPS/API, and Wattline UI asset checks.

Expected: exact package-driven state survives reboot with no manual module copy/load commands.

---

### Task 8: Publish and verify `v0.1.0`

**Files:**
- Git tag and GitHub release state only.

**Interfaces:**
- Consumes the exact router-validated main commit.
- Produces GitHub release `v0.1.0`, five IPK assets, `Packages`, `Packages.gz`, and gh-pages feed.

- [ ] **Step 1: Confirm tag/release absence and clean synchronization**

```bash
git fetch origin main --tags
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
test -z "$(git status --short)"
test -z "$(git tag --list v0.1.0)"
git ls-remote --exit-code --tags origin refs/tags/v0.1.0 && exit 1 || true
```

Expected: clean synchronized main and no existing tag.

- [ ] **Step 2: Create and push the release tag**

```bash
git tag -a v0.1.0 -m "Wattline 0.1.0"
git push origin v0.1.0
```

- [ ] **Step 3: Require release workflow success**

```bash
run_id="$(gh run list -R keithah/openwrt-wattline --workflow Release --limit 10 \
  --json databaseId,headBranch --jq 'map(select(.headBranch == "v0.1.0"))[0].databaseId')"
test -n "$run_id"
gh run watch "$run_id" -R keithah/openwrt-wattline --exit-status
```

Expected: Test, five-IPK/feed build, release publication, and gh-pages publication all succeed.

- [ ] **Step 4: Verify published release and feed artifacts**

```bash
gh release view v0.1.0 -R keithah/openwrt-wattline \
  --json tagName,targetCommitish,isDraft,isPrerelease,assets,url
```

Expected: non-draft/non-prerelease release at the validated commit with exactly:

```text
gl-app-wattline_0.1.0_all.ipk
luci-app-wattline_0.1.0_all.ipk
wattline-bt_0.1.0_all.ipk
wattline-rtl8761b_0.1.0_aarch64_cortex-a53.ipk
wattlined_0.1.0_aarch64_cortex-a53.ipk
Packages
Packages.gz
```

Download published assets to a temporary directory, compare their SHA-256 hashes to the locally router-validated artifacts, fetch the gh-pages `Packages.gz`, and confirm five records including the optional package.

- [ ] **Step 5: Final clean-state verification**

```bash
git fetch origin main --tags
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
test "$(git rev-list -n1 v0.1.0)" = "$(git rev-parse HEAD)"
test -z "$(git status --short)"
```

Expected: clean main, matching origin, tag on exact validated commit.
