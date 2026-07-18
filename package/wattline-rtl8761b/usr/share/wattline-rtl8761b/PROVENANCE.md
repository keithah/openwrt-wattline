# Wattline RTL8761B artifact provenance

This package contains a narrowly scoped Bluetooth driver backport for the
GL.iNet GL-X3000 running firmware 4.8.3 and its exact Linux `5.4.211` ABI.
The package admission checks deliberately refuse every other kernel release
and architecture.

## Kernel modules

- Upstream source: `linux-5.4.211.tar.xz` from
  `https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.4.211.tar.xz`
- Source archive SHA-256:
  `bfb43241b72cd55797af68bea1cebe630d37664c0f9a99b6e9263a63a67e2dec`
- Configuration: `router-4.8.3.config`, captured from `/proc/config.gz` on a
  GL-X3000 running GL.iNet firmware 4.8.3
- Cross compiler: `aarch64-linux-gnu-gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0`
- Source changes: `linux-5.4.211-rtl8761b-gl-abi.patch`
- License: GPL-2.0; see `COPYING`

The source patch makes three changes:

1. Backports RTL8761B firmware selection and project-ID handling in `btrtl`.
2. Marks USB IDs `2357:0604` and `0bda:8771` as Realtek devices before the
   generic USB class matcher in `btusb`.
3. Adds the eight-byte `struct timer_list` padding present in GL.iNet's MTK
   kernel ABI. Without this ABI compatibility adjustment, a vanilla module
   computes incorrect `struct hci_dev` offsets and corrupts memory.

Reproduction outline from an extracted pristine source tree:

```sh
patch -p1 < linux-5.4.211-rtl8761b-gl-abi.patch
cp router-4.8.3.config .config
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- olddefconfig
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- modules_prepare
make ARCH=arm64 CROSS_COMPILE=aarch64-linux-gnu- M=drivers/bluetooth modules
```

The shipped module hashes are recorded in `SHA256SUMS`. Each module reports
the exact vermagic `5.4.211 SMP mod_unload aarch64`.

## Firmware

- Upstream repository: `https://gitlab.com/kernel-firmware/linux-firmware`
- Upstream commit: `fb91c990e602c85f0bb2f98d89480c080fe63f68`
- Paths: `rtl_bt/rtl8761bu_fw.bin` and
  `rtl_bt/rtl8761bu_config.bin`
- License and attribution: see `WHENCE` and
  `LICENCE.rtlwifi_firmware.txt`

The bundled firmware was downloaded from that immutable commit and verified
byte-for-byte against the known-good files used during the GL-X3000 hardware
test. Its hashes are also recorded in `SHA256SUMS`.
