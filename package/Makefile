# Builds Wattline daemon, dependency, optional driver, and UI .ipk files
# without the full OpenWrt SDK.
# Requires: go (host), tar, gzip. Output in ./out.
#
# An OpenWrt .ipk is a GZIPPED TAR (not an ar/.deb archive) wrapping
# ./debian-binary + ./control.tar.gz + ./data.tar.gz. The router's opkg
# (verified on GL-X3000, opkg 2021-06-13) segfaults on the ar/.deb form.
VERSION := 0.1.0
ARCH := aarch64_cortex-a53
# GNU tar required for --owner/--group/--mode normalization (macOS: brew
# install gnu-tar); BSD tar lacks --mode and emits pax headers opkg rejects.
TAR := $(shell command -v gtar || command -v gnutar || echo tar)
OUT := out
BIN := $(OUT)/wattlined-bin

.PHONY: all clean bin ipk-wattlined ipk-bt ipk-rtl8761b ipk-luci ipk-glapp feed

all: ipk-wattlined ipk-bt ipk-rtl8761b ipk-luci ipk-glapp
	TAR="$(TAR)" ./check-ipk-metadata.sh $(OUT)/*.ipk

# feed: build all ipks + an opkg index (Packages/Packages.gz) so a router can
# `opkg update && opkg upgrade` from a hosted feed instead of manual reinstall.
# Bump VERSION (e.g. make VERSION=1.0.1 feed) so opkg sees an upgrade.
feed: all
	sh mkfeed.sh $(OUT)

bin:
	mkdir -p $(OUT)
	cd .. && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o package/$(BIN) ./cmd/wattlined

# Assemble an ipk from a staging dir: $1=pkgname $2=stagedir $3=arch
define make_ipk
	rm -rf $(OUT)/stage-$(1)
	cp -r $(2) $(OUT)/stage-$(1)
	# Normalize a restrictive or permissive host umask without widening the
	# credential directories shipped by wattlined.
	chmod -R u+rwX,go+rX,go-w $(OUT)/stage-$(1)
	if [ -d $(OUT)/stage-$(1)/etc/wattline ]; then \
		chmod 0700 $(OUT)/stage-$(1)/etc/wattline $(OUT)/stage-$(1)/etc/wattline/tls; \
	fi
	mkdir -p $(OUT)/stage-$(1)/CONTROL
	# Inject VERSION into the control's Version: field so the .ipk filename,
	# the control metadata, and the feed index all agree — otherwise opkg sees
	# "same version" and won't upgrade. (-i.bak works on BSD and GNU sed.)
	sed -i.bak "s/^Version:.*/Version: $(VERSION)/" $(OUT)/stage-$(1)/CONTROL/control
	rm -f $(OUT)/stage-$(1)/CONTROL/control.bak
	# data.tar.gz = everything except CONTROL.
	# --format ustar: OpenWrt's opkg tar parser rejects pax extended headers
	#   (typeflag 'x'/0x78), which macOS BSD tar emits by default.
	# COPYFILE_DISABLE: keep BSD tar from injecting AppleDouble (._*) entries.
	COPYFILE_DISABLE=1 $(TAR) --format ustar --owner=0 --group=0 --numeric-owner \
		--mode='go-w' --exclude=CONTROL -czf $(OUT)/data.tar.gz -C $(OUT)/stage-$(1) .
	COPYFILE_DISABLE=1 $(TAR) --format ustar --owner=0 --group=0 --numeric-owner \
		--mode='go-w' -czf $(OUT)/control.tar.gz -C $(OUT)/stage-$(1)/CONTROL .
	echo "2.0" > $(OUT)/debian-binary
	# Outer wrapper is a gzipped ustar tar (OpenWrt ipk format), NOT ar.
	( cd $(OUT) && COPYFILE_DISABLE=1 $(TAR) --format ustar -czf $(1)_$(VERSION)_$(3).ipk \
		./debian-binary ./control.tar.gz ./data.tar.gz )
	rm -f $(OUT)/data.tar.gz $(OUT)/control.tar.gz $(OUT)/debian-binary
endef

ipk-wattlined: bin
	rm -rf $(OUT)/stage
	mkdir -p $(OUT)/stage/usr/bin
	cp $(BIN) $(OUT)/stage/usr/bin/wattlined
	cp -r wattlined/etc $(OUT)/stage/
	cp -r wattlined/usr $(OUT)/stage/
	cp -r wattlined/CONTROL $(OUT)/stage/
	mkdir -p $(OUT)/stage/etc/wattline/tls
	chmod 0700 $(OUT)/stage/etc/wattline $(OUT)/stage/etc/wattline/tls
	chmod +x $(OUT)/stage/etc/init.d/wattlined $(OUT)/stage/etc/uci-defaults/99-wattline \
		$(OUT)/stage/etc/hotplug.d/iface/95-wattline $(OUT)/stage/usr/lib/wattline/firewall-sync \
		$(OUT)/stage/usr/lib/wattline/vpn-firewall-repair \
		$(OUT)/stage/CONTROL/postinst
	$(call make_ipk,wattlined,$(OUT)/stage,$(ARCH))

ipk-bt:
	$(call make_ipk,wattline-bt,wattline-bt,all)

# Optional, exact-kernel RTL8761B backport for GL-X3000 firmware 4.8.3.
# The package's preinst and driverctl reject every kernel except 5.4.211.
ipk-rtl8761b:
	rm -rf $(OUT)/stage-rtl8761b-source
	cp -r wattline-rtl8761b $(OUT)/stage-rtl8761b-source
	chmod 0755 \
		$(OUT)/stage-rtl8761b-source/CONTROL/preinst \
		$(OUT)/stage-rtl8761b-source/CONTROL/postinst \
		$(OUT)/stage-rtl8761b-source/CONTROL/prerm \
		$(OUT)/stage-rtl8761b-source/etc/init.d/wattline-rtl8761b \
		$(OUT)/stage-rtl8761b-source/etc/hotplug.d/usb/20-wattline-rtl8761b \
		$(OUT)/stage-rtl8761b-source/usr/lib/wattline/rtl8761b/driverctl
	chmod 0644 \
		$(OUT)/stage-rtl8761b-source/usr/lib/wattline/rtl8761b/modules/5.4.211/*.ko \
		$(OUT)/stage-rtl8761b-source/lib/firmware/rtl_bt/*.bin \
		$(OUT)/stage-rtl8761b-source/usr/share/wattline-rtl8761b/*
	$(call make_ipk,wattline-rtl8761b,$(OUT)/stage-rtl8761b-source,$(ARCH))

ipk-luci:
	$(call make_ipk,luci-app-wattline,luci-app-wattline,all)

# gl-app-wattline: native GL admin-panel (oui) app. The view is served gzipped
# from /www/views (nginx gzip_static serves it for the .js request oui makes),
# so gzip the source bundle into the staging tree.
ipk-glapp:
	rm -rf $(OUT)/stage-gl
	mkdir -p $(OUT)/stage-gl/www/views $(OUT)/stage-gl/usr/share/oui/menu.d $(OUT)/stage-gl/usr/lib/oui-httpd/rpc
	gzip -9 -c gl-app-wattline/www/views/gl-sdk4-ui-wattline.common.js > $(OUT)/stage-gl/www/views/gl-sdk4-ui-wattline.common.js.gz
	cp gl-app-wattline/usr/share/oui/menu.d/wattline.json $(OUT)/stage-gl/usr/share/oui/menu.d/
	cp gl-app-wattline/usr/lib/oui-httpd/rpc/wattline $(OUT)/stage-gl/usr/lib/oui-httpd/rpc/
	cp -r gl-app-wattline/CONTROL $(OUT)/stage-gl/
	chmod +x $(OUT)/stage-gl/CONTROL/postinst
	$(call make_ipk,gl-app-wattline,$(OUT)/stage-gl,all)

clean:
	rm -rf $(OUT)
