# Optional RTL8761B Driver Package Design

## Status

Approved in conversation on 2026-07-18. Implementation and the `v0.1.0`
release remain paused until this written specification is reviewed.

## Context

The GL-X3000 runs GL firmware with Linux `5.4.211`. Its stock
`kmod-bluetooth` supplies `btintel.ko` and `btusb.ko`, but `btusb` lacks the
Realtek firmware path, `btrtl.ko` is absent, and the stock firmware tree lacks
the RTL8761BU blobs. The TP-Link adapter on the validation router enumerates as
USB `2357:0604` and may re-enumerate as Realtek `0bda:8771` after firmware is
loaded.

The verified patched modules were cross-built from the edited Linux 5.4.211
tree at `/home/keith/src/rtl8761b-build/linux-5.4.211`. Their vermagic is
`5.4.211 SMP mod_unload aarch64`. The verified manual installation loaded
`btintel`, `btrtl`, and `btusb` in that order and used canonical
`rtl8761bu_fw.bin` and `rtl8761bu_config.bin` firmware.

The first attempted `0.1.0` package installation also exposed a separate
target compatibility defect: GL's overlay presents `/etc` and `/etc/config` as
`root:root 0775`, while certificate provisioning treated every group-write bit
as untrusted. The release must include the narrowly scoped fix that recognizes
GID 0 as privileged while continuing to reject world-write and non-root-group
write access.

## Goals

- Keep generic Bluetooth support independent of the RTL8761B workaround.
- Ship a self-contained, offline-installable package for the known GL-X3000
  kernel and RTL8761B/RTL8761BU USB IDs.
- Make installation, boot loading, hotplug, upgrade, and removal deterministic
  and recoverable.
- Fail before modifying modules when the kernel ABI, bundled artifacts, or
  target identity is incompatible.
- Validate the complete package-driven path on the current GL-X3000 rather
  than applying an undocumented manual repair.
- Publish the optional driver package with the other `v0.1.0` release assets
  and opkg feed metadata.

## Non-goals

- Supporting kernels other than exactly `5.4.211`.
- Building kernel modules on the router.
- Dynamically downloading firmware or kernel modules during installation.
- Replacing GL's complete `kmod-bluetooth` package.
- Activating the patched stack for generic CSR8510 or unrelated adapters.
- Claiming compatibility after a GL firmware upgrade that changes the kernel.

## Package Boundaries

### `wattline-bt`

`wattline-bt` remains the portable Bluetooth runtime package. It depends on
BlueZ, D-Bus, and GL's stock `kmod-bluetooth`. `wattlined` continues to depend
on `wattline-bt`. Neither package depends on the optional Realtek package, so a
router using a stock-supported adapter never receives patched kernel modules.

### `wattline-rtl8761b`

Add a fifth release package named
`wattline-rtl8761b_0.1.0_aarch64_cortex-a53.ipk`. It depends on
`wattline-bt` and contains:

- verified `btintel.ko`, `btrtl.ko`, and `btusb.ko` for Linux 5.4.211;
- canonical `rtl8761bu_fw.bin` and `rtl8761bu_config.bin`;
- an init/service helper that validates, installs, loads, verifies, and
  restores the driver stack;
- a USB hotplug entry for supported IDs;
- source patches, the relevant build configuration/provenance, licenses, and
  SHA-256 manifests required to trace and reproduce the binaries.

The firmware is bundled rather than downloaded. Router installation therefore
does not depend on WAN reachability or a mutable upstream URL, and the blobs
validated on the target are byte-identical to release assets.

## Detection and Admission Policy

The package is optional and is selected when hardware inspection finds either
supported USB identity:

- TP-Link UB500: `2357:0604`;
- Realtek post-firmware identity: `0bda:8771`.

The current validation router reports `2357:0604`, so the package will be
installed there. Documentation and any release installer must inspect
`/sys/bus/usb/devices/*/{idVendor,idProduct}` and install the fifth package only
for these IDs. Manual installation without a currently attached dongle is
allowed so a replacement adapter can be hotplugged later, but activation still
requires the exact kernel and valid artifacts.

Before changing the active stack, the helper must verify:

1. `uname -r` is exactly `5.4.211`;
2. the machine is AArch64 and the OpenWrt package architecture is compatible;
3. all bundled module and firmware hashes match the committed manifest;
4. each module reports vermagic `5.4.211 SMP mod_unload aarch64`;
5. required base Bluetooth modules and commands are present.

Any failure is fatal and leaves the stock module files and load configuration
unchanged.

## Installation and Activation

Patched modules are owned by the package under a private immutable directory,
not declared as overlapping payload files owned by `kmod-bluetooth`. This
avoids opkg file-ownership conflicts. Activation performs these steps under an
exclusive lock:

1. Record and preserve the original `/etc/modules.d/bluetooth` content.
2. Back up GL's original `btintel.ko` and `btusb.ko` once, including hashes and
   modes. Record that `btrtl.ko` was absent when applicable.
3. Copy patched modules to temporary files beside the active module paths,
   verify the temporary hashes, set normal module modes, and atomically rename
   them into place. Install firmware through package-owned paths under
   `/lib/firmware/rtl_bt`.
4. Remove automatic stock `btusb` loading from the generic Bluetooth module
   list without removing unrelated Bluetooth modules.
5. Stop `wattlined`, bring any HCI interface down, and unload `btusb`, `btrtl`,
   and `btintel` when safe.
6. Load the active modules explicitly in `btintel`, `btrtl`, `btusb` order.
7. Wait for the adapter, bring `hci0` up, and verify that the loaded module
   files and firmware-upload log correspond to the packaged stack.
8. Enable/restart BlueZ as required, then restart `wattlined`.

The operation is idempotent. Reinstallation or upgrade preserves the original
stock backup and reasserts the packaged hashes. A failure after backup restores
the prior module files and load configuration before returning nonzero.

## Boot and Hotplug

An early init service runs after the base Bluetooth kernel dependencies are
available and before `wattlined`. It validates that the active copies still
match the packaged modules, repairs them if a base-package upgrade replaced
them, and loads the three modules in the required order. The service tolerates
an absent dongle while keeping the patched driver ready.

The USB hotplug hook matches only `2357:0604` and `0bda:8771`. On attach it
serializes with the init helper, ensures the patched stack is active, raises
the HCI interface, and lets BlueZ/`wattlined` reconnect. Unrelated USB events
are no-ops.

## Removal and Recovery

Package removal stops users of the adapter, unloads the patched modules,
restores the exact backed-up stock `btintel.ko`, `btusb.ko`, and original
Bluetooth module-list content, removes a packaged `btrtl.ko` when it was not
originally present, runs dependency regeneration if available, and reloads the
stock Bluetooth stack. Package-owned firmware, hotplug, and init files are
removed normally by opkg.

Removal fails visibly rather than deleting the only recoverable stock backup.
The backup is deleted only after successful restoration. A documented manual
recovery command remains available if power is lost during activation.

## Build and Release

The known-good binaries, firmware, source patches, configuration/provenance,
licenses, and manifest are committed to the repository. The host-side package
build copies these committed inputs; CI does not depend on the developer's
external kernel tree.

`package/Makefile`, metadata checks, feed generation, README examples,
changelog, CI, and release workflow are updated from four to five IPKs. The
tag-derived version remains authoritative, and every filename and control
record for the new package must be `0.1.0` for this release.

The release sequence remains:

1. finish and verify the GL `/etc` certificate-parent compatibility fix;
2. build and verify all five IPKs locally;
3. push `main` and require CI success;
4. stream the exact local artifacts to the current router;
5. install the optional package because the router reports `2357:0604`;
6. complete on-target and reboot verification;
7. only then create and push tag `v0.1.0`;
8. require the release workflow and published assets/feed to succeed.

## Tests and Acceptance Evidence

Host-side tests must cover:

- supported and unsupported kernel admission;
- supported USB-ID detection and unrelated-adapter exclusion;
- exact module/firmware manifest and vermagic checks;
- staged activation order and rollback at every mutation boundary;
- idempotent reinstall and base-kmod overwrite repair;
- boot and hotplug behavior;
- uninstall restoration without destroying the stock backup;
- IPK contents, modes, architecture, version, and feed/release inventory;
- continued generic `wattline-bt` behavior without the optional package;
- the GL `root:root 0775` TLS-parent case while retaining unsafe-parent
  rejection.

On the current GL-X3000, record fresh evidence for:

- exact kernel and USB ID admission;
- package status `0.1.0` for all installed Wattline packages;
- packaged hashes at rest and active module hashes;
- `btintel`, `btrtl`, and `btusb` loaded in the expected stack;
- firmware upload/version in kernel logs;
- `hci0` `UP RUNNING` and nonempty BLE scan results;
- `wattlined` active, API reachable over the router address, and Link-Power
  connection behavior;
- reboot persistence of module hashes, load state, HCI, and daemon startup;
- a controlled remove/reinstall cycle proving stock restoration and package
  reactivation before the final installed state.

No release is published if any admission, rollback, reboot, or artifact check
fails.
