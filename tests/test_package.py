"""Check the installed LuCI/rpcd contract without an OpenWrt router."""
import json
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class PackageContract(unittest.TestCase):
    def test_menu_acl_and_rpc_match(self):
        root = ROOT / 'luci-app-firefox-vpn/root/usr/share'
        acl = json.loads((root / 'rpcd/acl.d/luci-app-firefox-vpn.json').read_text())
        menu = json.loads((root / 'luci/menu.d/luci-app-firefox-vpn.json').read_text())
        entry = menu['admin/services/firefox-vpn']
        self.assertEqual(entry['depends']['acl'], list(acl))
        rights = acl['luci-app-firefox-vpn']
        self.assertEqual(rights['read'], {'ubus': {'luci.firefox-vpn': ['read'], 'network.interface.lan': ['status']}})
        self.assertEqual(rights['write'], {'ubus': {'luci.firefox-vpn': ['write']}})
        view = ROOT / 'luci-app-firefox-vpn/htdocs/luci-static/resources/view'
        self.assertTrue((view / (entry['action']['path'] + '.js')).is_file())
        self.assertTrue((view / (entry['action']['path'] + '.css')).is_file())
        self.assertIn('$(LN) /usr/bin/firefox-vpn', (ROOT / 'firefox-vpn/Makefile').read_text())

    def test_defaults_and_local_source(self):
        config = json.loads((ROOT / 'firefox-vpn/files/config.json').read_text())
        self.assertEqual(config['proxy']['listen'], '0.0.0.0:1088')
        self.assertEqual(config['dashboard']['listen'], '127.0.0.1:10820')
        self.assertFalse(config['routing']['enabled'])
        self.assertEqual(config['proxy']['udp_mode'], 'dns')
        self.assertEqual(config['logging']['level'], 'info')
        source = ROOT / 'firefox-vpn/src'
        self.assertTrue((source / 'go.mod').is_file())
        module = 'firefox-vpn'
        self.assertEqual((source / 'go.mod').read_text().splitlines()[0], f'module {module}')
        self.assertFalse(list(source.rglob('*.svg')), 'native emoji flags do not need SVG assets')
        self.assertTrue((source / 'go.sum').is_file())
        self.assertTrue((source / 'cmd/firefox-vpn-openwrt/main.go').is_file())
        for removed in ('cmd/firefox-vpn-core', 'internal/desktop', 'internal/winsvc',
                        'internal/tun', 'mobile', 'tools/winres', 'internal/service/web'):
            self.assertFalse(any(path.is_file() for path in (source / removed).rglob('*')), removed)
        self.assertIn('Copyright (c) 2026 Arman', (source / 'LICENSE').read_text())
        package = (ROOT / 'firefox-vpn/Makefile').read_text()
        local = (ROOT / 'Makefile').read_text()
        self.assertIn(f'GO_PKG:={module}', package)
        self.assertNotIn('PKGARCH:=all', package)
        self.assertIn('LUCI_PKGARCH:=all', (ROOT / 'luci-app-firefox-vpn/Makefile').read_text())
        self.assertNotIn('PKG_SOURCE', package)
        self.assertIn('GO_PKG_BUILD_VARS+=CGO_ENABLED=0', package)
        self.assertIn('GOEXPERIMENT=nojsonv2', package)
        self.assertIn('GO_PKG_DEFAULT_LDFLAGS:=-linkmode internal', package)
        self.assertIn('GO_PKG_TAGS:=netgo osusergo http2legacy\n', package)
        self.assertIn('GO_PKG_LDFLAGS_X:=$(GO_PKG)/internal/version.Current=$(PKG_VERSION)-r$(PKG_RELEASE)', package)
        self.assertIn('internal/version.Current=$(PACKAGE_VERSION)', local)
        luci = (ROOT / 'luci-app-firefox-vpn/Makefile').read_text()
        self.assertNotIn('internal/service/web/flags', luci)
        self.assertIn('GO_TAGS := netgo,osusergo,http2legacy\n', local)
        self.assertIn('export GOEXPERIMENT := nojsonv2\n', local)
        self.assertNotIn('GO_PKG_INSTALL_EXTRA', package)
        self.assertNotIn('Build/Patch', package)
        self.assertIn('SOURCE := $(CURDIR)/firefox-vpn/src', local)
        self.assertNotIn('codeload.github.com', local)
        self.assertNotIn('source.mk', local + package)
        self.assertFalse((ROOT / 'firefox-vpn/source.mk').exists())
        self.assertFalse(list((ROOT / 'firefox-vpn/patches').glob('*.patch')))

    def test_published_signing_key_and_readme(self):
        workflow = (ROOT / '.github/workflows/build.yml').read_text()
        self.assertIn('APK_SIGNING_PUBLIC_KEY: ${{ vars.APK_SIGNING_PUBLIC_KEY }}', workflow)
        self.assertIn('*.pem', (ROOT / '.gitignore').read_text())
        readme = (ROOT / 'README.md').read_text()
        self.assertIn('/releases/latest', readme)
        self.assertIn('https://raw.githubusercontent.com/BercX/firefox-vpn-openwrt/refs/heads/master/install.sh', readme)
        self.assertNotIn('/releases/latest/download/install.sh', readme)
        installer = (ROOT / 'install.sh').read_text()
        self.assertIn('/releases/download/$TAG', installer)
        self.assertIn('"$URL/firefox-vpn-build-public.pem"', installer)
        self.assertIn('/etc/openwrt_release', installer)
        self.assertIn('DISTRIB_ARCH', installer)
        self.assertNotIn('OPENWRT_VERSION', installer + workflow)
        self.assertNotIn('@.release.version', installer)
        self.assertRegex(workflow, r'SDK_IMAGE: ghcr.io/openwrt/sdk:aarch64_cortex-a53-25.12.6@sha256:[0-9a-f]{64}')
        snippets = re.findall(r'```(sh|bash)\n(.*?)```', readme, re.S)
        self.assertTrue(snippets, 'README must include a checked installation command')
        for shell, script in snippets:
            subprocess.run([shell, '-n'], input=script, text=True, check=True)
        self.assertNotIn('--allow-untrusted', readme + installer)
        self.assertNotIn('.ipk', workflow + installer)
        self.assertNotIn('opkg install', readme)
        self.assertNotIn('experimental MASQUE', readme)
        self.assertFalse(list((ROOT / 'assets').rglob('*.svg')))

    def test_native_workflow(self):
        workflow = (ROOT / '.github/workflows/build.yml').read_text()
        self.assertEqual(workflow.count('run: make native-build BUILD_DIR='), 1)
        self.assertIn('run: make check test\n', workflow)
        self.assertNotIn('crosscheck', workflow)
        self.assertNotIn('matrix:', workflow)
        self.assertNotIn('gh-action-sdk', workflow)
        self.assertNotIn('$GITHUB_ENV', workflow, 'do not export signing keys to later steps')
        self.assertIn('cache-dependency-path: firefox-vpn/src/go.sum', workflow)
        self.assertRegex(workflow, r'on:\n  workflow_dispatch:\n\n')
        self.assertNotRegex(workflow, re.compile(r'^  (?:push|pull_request|pull_request_target|schedule):', re.M))
        build, signing = workflow.split('\n  sign:\n')
        self.assertNotIn('secrets.', build, 'the build job must never receive signing secrets')
        self.assertIn('needs: build', signing)
        self.assertIn("if: github.ref == 'refs/heads/master'", signing)
        self.assertIn('environment: signing', signing)
        self.assertEqual(workflow.count('secrets.APK_SIGNING_KEY'), 1)
        self.assertIn('python3 scripts/sign_apks.py', signing)
        for action in re.findall(r'uses: (\S+)', workflow):
            self.assertRegex(action, r'^actions/[\w-]+@[0-9a-f]{40}$')
        self.assertEqual(workflow.count('persist-credentials: false'), 2)
        self.assertIn('name: native-baselines', signing)
        self.assertEqual(workflow.count('uses: actions/upload-artifact@'), 1, 'only intermediate binaries use artifacts')
        self.assertIn('gh release create "$TAG" "$RELEASE_DIR"/*', signing)
        self.assertIn('--draft=false --latest', signing)
        self.assertIn('contents: write', signing)
        self.assertIn('needs: [build, sign]', workflow)
        self.assertIn('if: ${{ always() }}', workflow)
        self.assertIn('actions: write', workflow)
        self.assertIn('actions/runs/$GITHUB_RUN_ID/artifacts', workflow)


if __name__ == '__main__':
    unittest.main()
