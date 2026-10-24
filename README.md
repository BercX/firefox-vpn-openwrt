[![Version](https://img.shields.io/github/v/release/BercX/firefox-vpn-openwrt?label=version&display_name=release)](../../releases/latest)

# Firefox VPN for OpenWrt

Use Firefox's built-in VPN proxy on OpenWrt, managed through LuCI.
Routes traffic from configured clients—not your entire network automatically.
**Unofficial; not affiliated with Mozilla or the paid Mozilla VPN.**

<img width="800" alt="firefox-vpn-openwrt" src="https://github.com/user-attachments/assets/21e5522f-d88e-4ff9-bc00-00f4df0ab6e1" />


## Install

Requires **OpenWrt 25.12+ with LuCI** and an eligible Mozilla account.
Run over SSH:

```bash
sh <(wget -qO - https://raw.githubusercontent.com/BercX/firefox-vpn-openwrt/refs/heads/master/install.sh)
```

The installer verifies package signatures and starts the service.

## Usage

Open **Services > Firefox VPN** in LuCI and add your account.
Choose the location and optional proxy credentials in **Settings**, then use either option below.

**LAN devices:** Configure a browser, app or device that supports SOCKS5 or HTTP proxies to use
the router's **LAN IP**, port **1088**. Copy a ready-to-use URL from
**Overview > Proxy access**, or enter the address and credentials manually.
For SOCKS5, enable proxy-side DNS when supported. Only traffic configured to use
the proxy goes through it; other traffic keeps its normal connection.

**Router-side cores:** Use it as an upstream proxy in a core such as [sing-box](https://github.com/sagernet/sing-box)
(or another alternatives) running on the same router.
Configure a SOCKS5 or HTTP outbound to **127.0.0.1:1088**, with the proxy credentials if enabled.
Set routing rules in that core to choose which devices or destinations use this
outbound, without configuring each app individually. Traffic capture, DNS and
firewall rules remain the core's responsibility; this package does not set them up.

## Limitations

- **TCP:** Destination ports are generally limited to **80, 443, 8080, 8443**.
- **UDP:** Only DNS traffic is supported.
- **Routing:** No automatic routing or firewall changes.
- **Kill switch:** Not provided.

## Security

- **Internet exposure:** Do not expose proxy port **1088** to the internet.
- **LAN traffic:** Proxy authentication does not encrypt connections between your devices and the router.
- **Downloads:** Never bypass HTTPS or package signature errors.
- **Updates:** Update the backend and LuCI packages together.
- **Secrets:** Keep `/etc/firefox-vpn/` backups and authenticated proxy URLs private.

## Support Mozilla

This project relies on Firefox's free built-in VPN proxy. If you'd like to support
Mozilla's work on privacy, consider subscribing to
[Mozilla VPN](https://www.mozilla.org/products/vpn/)-a separate paid service
offering full-device VPN protection.


## Credits

- [armanlife/firefox-vpn-client](https://github.com/armanlife/firefox-vpn-client)
- [M-RTZ1/FoxyVPN](https://github.com/M-RTZ1/FoxyVPN)
- [Vauth/FoxyVPN](https://github.com/Vauth/FoxyVPN)
- [OpenWrt](https://github.com/openwrt/openwrt) and [LuCI](https://github.com/openwrt/luci)
