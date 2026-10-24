include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-firefox-vpn
PKG_VERSION:=0.1.0
PKG_RELEASE:=1
PKG_LICENSE:=MIT

LUCI_TITLE:=LuCI support for Firefox built-in VPN
LUCI_DEPENDS:=+firefox-vpn +luci-base
LUCI_PKGARCH:=all

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
