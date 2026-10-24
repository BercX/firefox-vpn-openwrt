#!/bin/sh
main() {
    set -eu

    REPO=BercX/firefox-vpn-openwrt

    fail() {
        echo "Error: $*" >&2
        exit 1
    }

    [ "$(id -u)" -eq 0 ] || fail "Run this installer as root."
    for command in jsonfilter apk wget; do
        command -v "$command" >/dev/null 2>&1 || fail "Required command not found: $command. Only APK-based OpenWrt is supported."
    done

    # APK may report only the CPU family, not OpenWrt's package architecture.
    ARCH="$(. /etc/openwrt_release && printf '%s' "${DISTRIB_ARCH:-}")" || fail "Cannot read /etc/openwrt_release to determine the package architecture."
    case "$ARCH" in
        ''|*[!a-zA-Z0-9_-]*) fail "Cannot determine a valid OpenWrt package architecture." ;;
    esac

    umask 077
    WORKDIR="$(mktemp -d /tmp/firefox-vpn-install.XXXXXX)"
    trap 'rm -rf "$WORKDIR"' EXIT
    trap 'exit 1' HUP INT TERM
    mkdir "$WORKDIR/keys"

    echo "Finding the latest Firefox VPN release for OpenWrt ($ARCH)..."
    wget -O "$WORKDIR/release.json" "https://api.github.com/repos/$REPO/releases/latest" || fail "Cannot fetch the latest release. Check https://github.com/$REPO/releases, internet access and router date/time."
    TAG="$(jsonfilter -i "$WORKDIR/release.json" -e '@.tag_name')"
    case "$TAG" in
        ''|*[!a-zA-Z0-9._-]*) fail "No valid published release tag was found. Check https://github.com/$REPO/releases." ;;
    esac
    VERSION="${TAG#v}"
    case "$VERSION" in
        [0-9]*-r[0-9]*) ;;
        *) fail "Release tag must include the package version and revision (for example 0.1.0-r1)." ;;
    esac
    BACKEND="firefox-vpn-$VERSION-$ARCH.apk"
    LUCI="luci-app-firefox-vpn-$VERSION-all.apk"
    URL="https://github.com/$REPO/releases/download/$TAG"
    for package in "$BACKEND" "$LUCI"; do
        wget -O "$WORKDIR/$package" "$URL/$package" || fail "Package unavailable or download failed: $package."
        [ -s "$WORKDIR/$package" ] || fail "Downloaded package is empty: $package."
    done
    wget -O "$WORKDIR/keys/firefox-vpn-build.pem" "$URL/firefox-vpn-build-public.pem" || fail "Cannot download the package signing key."

    # Verify both packages before trusting the key or changing the installation.
    apk --keys-dir "$WORKDIR/keys" verify "$WORKDIR/$BACKEND" "$WORKDIR/$LUCI" || fail "Package signature verification failed. Nothing was installed."
    apk update || fail "Cannot update OpenWrt repositories. Check internet access and router date/time."
    mkdir -p /etc/apk/keys
    cp "$WORKDIR/keys/firefox-vpn-build.pem" /etc/apk/keys/firefox-vpn-build.pem
    apk add "$WORKDIR/$BACKEND" "$WORKDIR/$LUCI" || fail "Package installation failed. Check free space and dependency errors above."

    /etc/init.d/firefox-vpn enable
    /etc/init.d/firefox-vpn restart
    /etc/init.d/rpcd restart
    echo "Installed $TAG. Open Services > Firefox VPN in LuCI to add your account."
}

main "$@"
