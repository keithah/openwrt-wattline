# Making an RTL8761B dongle (TP-Link UB500) work on a GL-X3000 (kernel 5.4.211)

**Status: advanced / experimental.** If you can get a **CSR8510** dongle
(TP-Link **UB400**, or any generic "CSR 4.0" adapter), use that instead — it
works out of the box with the stock `btusb` and none of this is needed. This
guide is for when you're stuck with an RTL8761B (e.g. Amazon shipped a UB500
instead of a UB400).

## Why it doesn't just work

The GL-X3000 (Spitz AX) runs OpenWrt/GL 4.9.0 on **kernel 5.4.211**. The RTL8761B
needs the kernel to upload patch firmware to the chip via the `btrtl` driver and
the RTL glue in `btusb`. On this kernel:

- `CONFIG_BT_HCIBTUSB_RTL` is **not set** — `btusb` has no RTL firmware-load path.
- `btrtl.ko` is **not present** at all.
- Even the 5.4 `btrtl` source only knows the **RTL8761A**, not the **8761B**
  (8761B support was added upstream around Linux **5.8**).
- No RTL Bluetooth firmware ships in `/lib/firmware/rtl_bt/`, and there's no RTL
  kmod in the GL feed.

Result: the dongle enumerates and `hci0` comes up (`UP RUNNING`, valid MAC), but
it receives **zero** advertisements — `hcitool lescan` finds nothing — because
its firmware was never loaded.

## Why sideloading is nonetheless possible

The running kernel is friendly to foreign modules:

```
# CONFIG_MODVERSIONS is not set   <- no symbol-CRC check
# CONFIG_MODULE_SIG is not set    <- no signature check
CONFIG_MODULE_UNLOAD=y
```

So a module built elsewhere will load as long as its **vermagic matches exactly**:

```
vermagic: 5.4.211 SMP mod_unload aarch64
```

vermagic here is just `UTS_RELEASE + SMP + mod_unload + arch` — no gcc/config
hash — so you don't need GL's exact `.config`, only a 5.4.211 arm64 tree built
`SMP`, `MODULE_UNLOAD=y`, `MODVERSIONS=n`.

## Build plan (on a Linux box)

You need `btusb.ko` (with RTL support) and `btrtl.ko` (with the RTL8761B entry
backported), built for kernel **5.4.211 aarch64**.

### Option A — OpenWrt SDK (recommended)

1. Get the OpenWrt **21.02** SDK for the MediaTek **filogic** (MT7981) target, or
   the GL-iNet SDK for the GL-X3000 (https://github.com/gl-inet/sdk). Confirm its
   kernel is **5.4.211**.
2. In the kernel config, enable:
   ```
   CONFIG_BT_RTL=y            (or =m -> btrtl.ko)
   CONFIG_BT_HCIBTUSB_RTL=y
   ```
   (menuconfig: Networking support -> Bluetooth -> Bluetooth device drivers ->
   HCI USB driver -> "Realtek protocol support".)
3. **Backport RTL8761B** into `drivers/bluetooth/btrtl.c` — the 5.4 driver only
   has the 8761A. Add an 8761B device-table entry and firmware names. Reference
   the upstream commits that added 8761B/8761BU support (Linux ~5.8+); adapt them
   to the 5.4 `ic_id_table` struct format (the 5.4 entries use
   `IC_MATCH_FL_LMPSUBV` / `IC_MATCH_FL_HCIREV`). The 8761B matches
   `lmp_subver 0x8761` with a distinct `hci_rev`, `config_needed = true`,
   `fw_name = "rtl_bt/rtl8761b_fw.bin"`, `cfg_name = "rtl_bt/rtl8761b_config.bin"`.
   Also confirm `btusb.c`'s device table binds the dongle's USB id
   (`2357:0604` for the UB500, which re-enumerates as `0bda:8771` once firmware
   loads) to the RTL setup path.
4. Build just the bluetooth kmods (e.g. `make package/kernel/linux/compile` with
   kmod-bluetooth selected) and pull `btusb.ko` + `btrtl.ko` out of the SDK's
   `bin/targets/.../linux-*/` staging.
5. Verify vermagic: `modinfo btusb.ko | grep vermagic` must print
   `5.4.211 SMP mod_unload aarch64`.

### Option B — vanilla 5.4.211 tree

Build `drivers/bluetooth/{btusb,btrtl}.ko` out-of-tree against a plain
kernel.org **5.4.211** source, configured `ARCH=arm64`, `CONFIG_SMP=y`,
`CONFIG_MODULE_UNLOAD=y`, `CONFIG_MODVERSIONS=n`, with the RTL options above and
the same 8761B backport, using an `aarch64` cross toolchain. vermagic must match.

## Firmware

```sh
./fetch-firmware.sh            # downloads + renames into ./rtl_bt/
scp -r rtl_bt root@ROUTER:/lib/firmware/
```

(Downloads `rtl8761b_fw` and `rtl8761b_config` from Realtek's repo and renames
them to `rtl8761b_fw.bin` / `rtl8761b_config.bin`.)

## Sideload onto the router

```sh
# copy the two modules
scp btrtl.ko btusb.ko root@ROUTER:/lib/modules/5.4.211/
ssh root@ROUTER '
  depmod -a
  rmmod btusb 2>/dev/null
  insmod /lib/modules/5.4.211/btrtl.ko
  insmod /lib/modules/5.4.211/btusb.ko
  sleep 2
  dmesg | tail -20            # expect: rtl8761b firmware loaded, hci0 reset
  hciconfig hci0 up
  hcitool lescan --duplicates # should now list nearby BLE devices
'
```

To make it persist across reboots, drop the two `.ko` into
`/etc/modules.d/` load order or a small init script (OpenWrt loads
`/etc/modules.d/*` at boot).

## Verifying with Wattline

Once `hcitool lescan` shows the `Link-Power-2`, start the daemon and it will
connect:

```sh
ssh root@ROUTER '/etc/init.d/wattlined start; sleep 10; logread -e wattline | tail'
```

## Known unknowns

- The 8761B backport into a 5.4 `btrtl` is the fiddly part; the struct layout
  differs from newer kernels. Budget time to iterate.
- If the firmware uploads but the chip still won't scan, double-check the config
  blob name/version — some UB500 revisions want `rtl8761bu_fw.bin`.
- vermagic must be exact; a mismatch fails `insmod` with "version magic ...
  should be ...".
