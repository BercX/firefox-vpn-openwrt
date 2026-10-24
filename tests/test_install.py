"""Run the installer against fake router commands, without network or root writes."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
MOCK = r'''#!/usr/bin/env python3
import json, os, pathlib, re, sys
name, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
root = pathlib.Path(os.environ['TEST_ROOT'])
scenario = os.environ['SCENARIO']
with (root / 'commands.jsonl').open('a') as log:
    log.write(json.dumps([name, *args]) + '\n')
if name == 'id':
    print('1000' if scenario == 'not-root' else '0')
elif name == 'ubus':
    print(json.dumps({'release': {'version': os.environ['TEST_VERSION']}}))
elif name == 'jsonfilter':
    value = json.loads(pathlib.Path(args[args.index('-i') + 1]).read_text() if '-i' in args else sys.stdin.read())
    expression = args[args.index('-e') + 1]
    match = re.fullmatch(r'@\.assets\[@\.name="([^"]+)"\]\.id', expression)
    if match:
        for asset in value.get('assets', []):
            if asset['name'] == match[1]:
                print(asset['id'])
    else:
        for key in expression.removeprefix('@.').split('.'):
            value = value.get(key, '') if isinstance(value, dict) else ''
        print(value)
elif name == 'apk':
    if args == ['--print-arch']:
        print('x86_64' if scenario == 'x86' else 'aarch64')
    elif (scenario in ('bad-signature', 'bad-signature-api') and 'verify' in args) or (scenario == 'install-failed' and args[0] == 'add'):
        sys.exit(1)
elif name == 'wget':
    output, url = pathlib.Path(args[args.index('-O') + 1]), args[-1]
    api = '/releases/assets/' in url
    if api:
        assert '--header=Accept: application/octet-stream' in args
        assert not output.exists(), 'partial download was not removed before fallback'
    if scenario == 'download-failed' and ('luci-app' in url or url.endswith('/assets/102')):
        output.write_text('partial')
        sys.exit(1)
    if url.endswith('/releases/latest'):
        if scenario == 'release-unavailable':
            sys.exit(1)
        tag = {'no-release': '', 'versionless-release': '0.1.0', 'new-revision': '0.2.0-r42',
               'legacy-tag': 'v0.1.0-r13', 'invalid-metadata': '0.1.0+r1+extra'}.get(scenario, '0.1.0-r1')
        architecture = (root / 'openwrt_release').read_text().split("'")[1]
        names = [f'firefox-vpn-{tag.removeprefix("v")}-{architecture}.apk',
                 f'luci-app-firefox-vpn-{tag.removeprefix("v")}-all.apk', 'firefox-vpn-build-public.pem']
        assets = [{'name': name, 'id': 101 + i} for i, name in enumerate(names)]
        if scenario == 'missing-asset':
            assets = assets[1:]
        if scenario == 'invalid-asset-id':
            assets[0]['id'] = '../bad'
        output.write_text(json.dumps({'tag_name': tag, 'assets': assets}))
    else:
        if scenario in ('api-fallback', 'api-failed', 'api-empty', 'bad-signature-api',
                        'missing-asset', 'invalid-asset-id') and not api:
            output.write_text('partial')
            sys.exit(4)
        if (scenario == 'empty-direct' and not api) or (scenario == 'api-empty' and api):
            output.write_text('')
            sys.exit(0)
        if scenario == 'api-failed' and api:
            output.write_text('partial')
            sys.exit(4)
        output.write_text('fixture')
'''


class InstallerCheck(unittest.TestCase):
    def test_truncated_download_does_not_start_installer(self):
        lines = (ROOT / 'install.sh').read_text().splitlines(keepends=True)
        self.assertEqual(lines[-1].strip(), 'main "$@"')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / 'started'
            mock = root / 'id'
            mock.write_text('#!/bin/sh\necho started > "$MARKER"\necho 1000\n')
            mock.chmod(0o755)
            for end in range(1, len(lines)):
                with self.subTest(lines_received=end):
                    subprocess.run(['sh'], input=''.join(lines[:end]), capture_output=True, text=True,
                                   env={**os.environ, 'PATH': directory + ':' + os.environ['PATH'],
                                        'MARKER': str(marker)})
                    self.assertFalse(marker.exists(), 'partial download started the installer')

    def test_install_and_reject_unsupported_or_failed_downloads(self):
        for scenario, version in [
            ('ok', '25.12.6'), ('not-root', '25.12.6'),
            ('ok', '25.12.5'), ('ok', 'SNAPSHOT'),
            ('ok', '25.12.6-rc1'), ('ok', '26.01.0'),
            ('bad-arch', '25.12.6'), ('empty-arch', '25.12.6'),
            ('missing-release-file', '25.12.6'), ('no-release', '25.12.6'),
            ('release-unavailable', '25.12.6'),
            ('versionless-release', '25.12.6'), ('new-revision', '25.12.6'),
            ('legacy-tag', '25.12.6'), ('invalid-metadata', '25.12.6'),
            ('x86', '25.12.6'), ('arm64-generic', '25.12.6'), ('arm64-a72', '25.12.6'),
            ('download-failed', '25.12.6'), ('bad-signature', '25.12.6'),
            ('install-failed', '25.12.6'),
            ('api-fallback', '25.12.6'), ('empty-direct', '25.12.6'),
            ('api-failed', '25.12.6'), ('api-empty', '25.12.6'),
            ('missing-asset', '25.12.6'), ('invalid-asset-id', '25.12.6'),
            ('bad-signature-api', '25.12.6'),
        ]:
            with self.subTest(scenario=scenario, firmware=version), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                architecture = {'x86': 'x86_64', 'arm64-generic': 'aarch64_generic',
                                'arm64-a72': 'aarch64_cortex-a72', 'bad-arch': '../bad',
                                'empty-arch': ''}.get(scenario, 'aarch64_cortex-a53')
                if scenario != 'missing-release-file':
                    (root / 'openwrt_release').write_text(f"DISTRIB_ARCH='{architecture}'\n")
                binaries = root / 'bin'
                binaries.mkdir()
                mock = binaries / 'mock'
                mock.write_text(MOCK)
                mock.chmod(0o755)
                for name in ('id', 'ubus', 'jsonfilter', 'apk', 'wget', 'firefox-vpn', 'rpcd'):
                    (binaries / name).symlink_to(mock)
                # Redirect only filesystem locations; execute the real installer logic.
                source = (ROOT / 'install.sh').read_text().replace(
                    '/etc/openwrt_release', str(root / 'openwrt_release')).replace(
                    '/etc/apk/keys', str(root / 'keys')).replace(
                    '/etc/init.d/', str(binaries) + '/').replace(
                    '/tmp/firefox-vpn-install.', str(root / 'download.'))
                installer = root / 'install.sh'
                installer.write_text(source)
                result = subprocess.run(['sh', str(installer)], capture_output=True, text=True,
                                        env={**os.environ, 'PATH': str(binaries) + ':' + os.environ['PATH'],
                                             'TEST_ROOT': directory, 'SCENARIO': scenario, 'TEST_VERSION': version})
                success = scenario in ('ok', 'new-revision', 'legacy-tag', 'x86', 'arm64-generic', 'arm64-a72',
                                       'api-fallback', 'empty-direct')
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                commands = [json.loads(line) for line in (root / 'commands.jsonl').read_text().splitlines()]
                installs = [cmd for cmd in commands if cmd[:2] == ['apk', 'add']]
                services = [cmd for cmd in commands if cmd[0] in ('firefox-vpn', 'rpcd')]
                self.assertEqual(bool(installs), success or scenario == 'install-failed')
                if success:
                    self.assertEqual(services, [['firefox-vpn', 'enable'], ['firefox-vpn', 'restart'], ['rpcd', 'restart']])
                    downloads = [cmd for cmd in commands if cmd[0] == 'wget'][1:]
                    urls = [cmd[-1] for cmd in downloads if '/releases/download/' in cmd[-1]]
                    self.assertEqual(next(cmd[-1] for cmd in commands if cmd[0] == 'wget'),
                                     'https://api.github.com/repos/BercX/firefox-vpn-openwrt/releases/latest')
                    package_version = '0.2.0-r42' if scenario == 'new-revision' else '0.1.0-r13' if scenario == 'legacy-tag' else '0.1.0-r1'
                    tag = ('v' if scenario == 'legacy-tag' else '') + package_version
                    self.assertEqual([url.rsplit('/', 1)[1] for url in urls], [
                        f'firefox-vpn-{package_version}-{architecture}.apk',
                        f'luci-app-firefox-vpn-{package_version}-all.apk',
                        'firefox-vpn-build-public.pem'])
                    self.assertTrue(all(f'/download/{tag}/' in url for url in urls))
                    self.assertEqual([Path(path).name for path in installs[0][2:]], [url.rsplit('/', 1)[1] for url in urls[:2]])
                    self.assertTrue((root / 'keys/firefox-vpn-build.pem').is_file())
                    if scenario in ('api-fallback', 'empty-direct'):
                        self.assertEqual([cmd[-1] for cmd in downloads], [
                            url for i, direct in enumerate(urls) for url in (
                                direct, f'https://api.github.com/repos/BercX/firefox-vpn-openwrt/releases/assets/{101 + i}')])
                    else:
                        self.assertEqual(len(downloads), 3, 'successful direct downloads must not use the API')
                    verification = next(i for i, cmd in enumerate(commands) if cmd[0] == 'apk' and 'verify' in cmd)
                    self.assertEqual(commands[verification][-2:], installs[0][2:])
                    self.assertLess(verification, commands.index(installs[0]))
                else:
                    self.assertFalse(services)
                if not success and scenario != 'install-failed':
                    self.assertFalse((root / 'keys').exists())
                if scenario in ('not-root', 'bad-arch', 'empty-arch', 'missing-release-file'):
                    self.assertFalse(any(cmd[0] == 'wget' for cmd in commands))
                if scenario in ('release-unavailable', 'no-release'):
                    self.assertIn('https://github.com/BercX/firefox-vpn-openwrt/releases', result.stderr)
                if scenario in ('missing-asset', 'invalid-asset-id'):
                    self.assertFalse(any('/releases/assets/' in cmd[-1] for cmd in commands if cmd[0] == 'wget'))
                for cmd in commands:
                    if cmd[0] == 'wget':
                        self.assertEqual(cmd[cmd.index('-T') + 1], '20')
                        self.assertNotIn('--no-check-certificate', cmd)
                self.assertFalse(list(root.glob('download.*')), 'temporary files were not cleaned up')


if __name__ == '__main__':
    unittest.main()
