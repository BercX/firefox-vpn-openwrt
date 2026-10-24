include $(TOPDIR)/rules.mk

PKG_NAME:=firefox-vpn
PKG_VERSION:=0.1.0
PKG_RELEASE:=1
PKG_LICENSE:=MIT
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=firefox-vpn
GO_PKG_BUILD_PKG:=$(GO_PKG)/cmd/firefox-vpn-openwrt
# Go 1.27's x/net wrapper rejects :protocol; the pinned legacy transport supports it.
GO_PKG_TAGS:=netgo osusergo http2legacy
GO_PKG_LDFLAGS_X:=$(GO_PKG)/internal/version.Current=$(PKG_VERSION)-r$(PKG_RELEASE)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# No libc ABI dependency in the Go binary: reuse it across firmware releases.
GO_PKG_BUILD_VARS+=CGO_ENABLED=0 GOEXPERIMENT=nojsonv2
GO_PKG_DEFAULT_LDFLAGS:=-linkmode internal

define Package/firefox-vpn
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=Firefox built-in VPN proxy for OpenWrt
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle +rpcd
endef

define Package/firefox-vpn/description
  Unofficial Firefox built-in VPN SOCKS5/HTTP proxy, supervised by procd.
  Includes a restricted rpcd bridge for LuCI and interactive account sign-in.
endef

define Package/firefox-vpn/conffiles
/etc/firefox-vpn/
/etc/firefox-vpn/config.json
endef

define Package/firefox-vpn/install
	$(INSTALL_DIR) $(1)/usr/bin $(1)/usr/libexec/rpcd $(1)/etc/init.d
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/firefox-vpn-openwrt $(1)/usr/bin/firefox-vpn
	$(LN) /usr/bin/firefox-vpn $(1)/usr/libexec/rpcd/luci.firefox-vpn
	$(INSTALL_BIN) ./files/firefox-vpn.init $(1)/etc/init.d/firefox-vpn
	$(INSTALL_DIR) $(1)/etc/firefox-vpn $(1)/usr/share/firefox-vpn
	chmod 0700 $(1)/etc/firefox-vpn
	$(INSTALL_CONF) ./files/config.json $(1)/etc/firefox-vpn/config.json
	$(INSTALL_DATA) ./files/LICENSE $(1)/usr/share/firefox-vpn/LICENSE
	$(INSTALL_DATA) ./src/LICENSE $(1)/usr/share/firefox-vpn/LICENSE.upstream
	$(INSTALL_DATA) ../README.md $(1)/usr/share/firefox-vpn/README.md
endef

$(eval $(call GoBinPackage,firefox-vpn))
$(eval $(call BuildPackage,firefox-vpn))
