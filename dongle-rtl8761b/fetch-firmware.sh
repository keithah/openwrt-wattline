#!/bin/sh
# Download the RTL8761B Bluetooth firmware and stage it for the router.
# Copy the resulting rtl_bt/ dir to the router's /lib/firmware/.
set -eu
DEST="${1:-./rtl_bt}"
mkdir -p "$DEST"
BASE="https://raw.githubusercontent.com/Realtek-OpenSource/android_hardware_realtek/rtk1395/bt/rtkbt/Firmware/BT"
echo "Fetching RTL8761B firmware into $DEST ..."
curl -fSL "$BASE/rtl8761b_fw"     -o "$DEST/rtl8761b_fw.bin"
curl -fSL "$BASE/rtl8761b_config" -o "$DEST/rtl8761b_config.bin"
ls -l "$DEST"
echo "Now copy $DEST to the router: scp -r $DEST root@ROUTER:/lib/firmware/"
