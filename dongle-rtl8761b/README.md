# RTL8761B Bluetooth on the GL-X3000

Wattline ships an optional `wattline-rtl8761b` package for the TP-Link UB500
and compatible Realtek adapters on the GL-X3000 running Linux `5.4.211`.
CSR8510 adapters such as the TP-Link UB400 use the stock driver and must not
install this package.

## Select the package from hardware identity

Read the adapter identity from sysfs; do not guess from its product name:

```sh
for d in /sys/bus/usb/devices/*; do
  [ -r "$d/idVendor" ] && [ -r "$d/idProduct" ] || continue
  printf '%s:%s\n' "$(tr A-F a-f < "$d/idVendor")" \
    "$(tr A-F a-f < "$d/idProduct")"
done
```

Install `wattline-rtl8761b` only if that prints `2357:0604` or `0bda:8771`:

```sh
opkg install wattline-rtl8761b
```

For a local IPK, use
`opkg install /tmp/wattline-rtl8761b_0.1.0_aarch64_cortex-a53.ipk`.
The package depends on `wattline-bt`, so opkg installs the ordinary BlueZ and
Bluetooth dependencies first.

Installation is deliberately rejected unless both of these are exact:

```text
kernel:       5.4.211
architecture: aarch64
```

The package is not compatible with a firmware upgrade that changes the kernel,
even if the new kernel is also an aarch64 build. Rebuild the modules/package for
that exact kernel ABI before upgrading.

## What the package owns

The IPK contains everything specific to this adapter:

- hash-pinned `btintel.ko`, `btrtl.ko`, and `btusb.ko` in a private immutable
  payload;
- canonical `rtl8761bu_fw.bin` and `rtl8761bu_config.bin` in
  `/lib/firmware/rtl_bt`;
- an exact source patch, router build config, hashes, licenses, and provenance;
- transactional activation/restoration logic;
- an init service that loads the modules before `wattlined`; and
- a USB hotplug hook for the two supported IDs.

On first activation it backs up the stock module files and
`/etc/modules.d/bluetooth`, then atomically installs the packaged modules,
removes only the stock `btusb` autoload line, and loads
`btintel` → `btrtl` → `btusb`. Reinstall and boot repair a later stock overwrite
without replacing the original backup. Removing the package restores the stock
files before opkg removes the private payload.

Useful diagnostics:

```sh
/usr/lib/wattline/rtl8761b/driverctl detect
/usr/lib/wattline/rtl8761b/driverctl admit
/usr/lib/wattline/rtl8761b/driverctl status
hciconfig -a
logread -e wattline
```

`driverctl status` reports `stock`, `packaged`, `drifted`, or `unsupported`.
Do not copy replacement modules directly into `/lib/modules`; that bypasses the
backup, hash verification, rollback, boot ordering, and package removal path.

## Why the backport is necessary

GL.iNet firmware 4.8.3 uses Linux 5.4.211, whose Bluetooth stack predates the
RTL8761B support required by the UB500. The backport selects the Realtek setup
path for USB IDs `2357:0604` and `0bda:8771`, selects the canonical 8761BU
firmware, and adds the RTL project mapping.

The GL/MTK kernel also has an eight-byte-larger `struct timer_list` than vanilla
5.4.211. Modules built without that padding compute incorrect `struct hci_dev`
offsets, causing `hci_register_dev` failures and kernel memory corruption. The
bundled modules include the GL ABI padding and have exact vermagic:

```text
5.4.211 SMP mod_unload aarch64
```

Full source and firmware provenance is installed under
`/usr/share/wattline-rtl8761b/`.
