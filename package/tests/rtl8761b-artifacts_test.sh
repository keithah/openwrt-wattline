#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
BASE="$ROOT/package/wattline-rtl8761b"
MOD="$BASE/usr/lib/wattline/rtl8761b/modules/5.4.211"
FW="$BASE/lib/firmware/rtl_bt"
DOC="$BASE/usr/share/wattline-rtl8761b"

check_hash() {
	expected="$1"
	path="$2"
	printf '%s  %s\n' "$expected" "$path" | sha256sum -c -
}

check_hash 82a811239f2530aedc2fa9eca79276b517241b9a204f42b84d26af9c3a8e41c1 "$MOD/btusb.ko"
check_hash ef80e6fdcb5affe3556db6933a216babe2b96a66cf8aae00ee467d5d13f3dbb6 "$MOD/btrtl.ko"
check_hash 4237ce29b705e9b1d00d7cba064ba5d01bec89756f7a98176ecf029390fa2bf9 "$MOD/btintel.ko"
check_hash 1d7a9597349ad89344fa16c1913d3e39e9a12e966e417ca16871bc79bbe59edb "$FW/rtl8761bu_fw.bin"
check_hash 6c28a3f07c6a30ed208c4b64862a23f02b7d93543ea980edd24df16bab45095f "$FW/rtl8761bu_config.bin"

for module in btintel btrtl btusb; do
	[ "$(modinfo -F vermagic "$MOD/$module.ko" | sed 's/[[:space:]]*$//')" = '5.4.211 SMP mod_unload aarch64' ]
done
modinfo "$MOD/btusb.ko" | grep -Fqi 'usb:v2357p0604'
modinfo "$MOD/btusb.ko" | grep -Fqi 'usb:v0BDAp8771'

for name in SHA256SUMS PROVENANCE.md COPYING WHENCE LICENCE.rtlwifi_firmware.txt \
	linux-5.4.211-rtl8761b-gl-abi.patch router-4.8.3.config; do
	test -s "$DOC/$name"
done

(cd "$BASE" && sha256sum -c usr/share/wattline-rtl8761b/SHA256SUMS)
grep -Fq 'rtl_bt/rtl8761bu_fw.bin' "$DOC/WHENCE"
grep -Fq 'rtl_bt/rtl8761bu_config.bin' "$DOC/WHENCE"
grep -Fq 'include/linux/timer.h' "$DOC/linux-5.4.211-rtl8761b-gl-abi.patch"
grep -Fq 'drivers/bluetooth/btrtl.c' "$DOC/linux-5.4.211-rtl8761b-gl-abi.patch"
grep -Fq 'drivers/bluetooth/btusb.c' "$DOC/linux-5.4.211-rtl8761b-gl-abi.patch"

echo "RTL8761B artifact tests passed"
