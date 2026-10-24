"""Run the installer against fake router commands, without network or root writes."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
MOCK = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
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
    for key in args[args.index('-e') + 1].removeprefix('@.').split('.'):
        value = value.get(key, '') if isinstance(value, dict) else ''
    print(value)
elif name == 'apk':
    if args == ['--print-arch']:
        print({'bad-arch': '../bad', 'x86': 'x86_64'}.get(scenario, 'aarch64_cortex-a53'))
    elif (scenario == 'bad-signature' and 'verify' in args) or (scenario == 'install-failed' and args[0] == 'add'):
        sys.exit(1)
elif name == 'wget':
    output, url = pathlib.Path(args[args.index('-O') + 1]), args[-1]
    if scenario == 'download-failed' and 'luci-app' in url:
        output.write_text('partial')
        sys.exit(1)
    if url.endswith('/releases/latest'):
        if scenario == 'release-unavailable':
            sys.exit(1)
        tag = {'no-release': '', 'versionless-release': '0.1.0', 'new-revision': '0.2.0-r42',
               'legacy-tag': 'v0.1.0-r13', 'invalid-metadata': '0.1.0+r1+extra'}.get(scenario, '0.1.0-r1')
        output.write_text(json.dumps({'tag_name': tag}))
    else:
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
            ('bad-arch', '25.12.6'), ('no-release', '25.12.6'),
            ('release-unavailable', '25.12.6'),
            ('versionless-release', '25.12.6'), ('new-revision', '25.12.6'),
            ('legacy-tag', '25.12.6'), ('invalid-metadata', '25.12.6'),
            ('x86', '25.12.6'),
            ('download-failed', '25.12.6'), ('bad-signature', '25.12.6'),
            ('install-failed', '25.12.6'),
        ]:
            with self.subTest(scenario=scenario, firmware=version), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binaries = root / 'bin'
                binaries.mkdir()
                mock = binaries / 'mock'
                mock.write_text(MOCK)
                mock.chmod(0o755)
                for name in ('id', 'ubus', 'jsonfilter', 'apk', 'wget', 'firefox-vpn', 'rpcd'):
                    (binaries / name).symlink_to(mock)
                # Redirect only filesystem locations; execute the real installer logic.
                source = (ROOT / 'install.sh').read_text().replace(
                    '/etc/apk/keys', str(root / 'keys')).replace(
                    '/etc/init.d/', str(binaries) + '/').replace(
                    '/tmp/firefox-vpn-install.', str(root / 'download.'))
                installer = root / 'install.sh'
                installer.write_text(source)
                result = subprocess.run(['sh', str(installer)], capture_output=True, text=True,
                                        env={**os.environ, 'PATH': str(binaries) + ':' + os.environ['PATH'],
                                             'TEST_ROOT': directory, 'SCENARIO': scenario, 'TEST_VERSION': version})
                success = scenario in ('ok', 'new-revision', 'legacy-tag', 'x86')
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                commands = [json.loads(line) for line in (root / 'commands.jsonl').read_text().splitlines()]
                installs = [cmd for cmd in commands if cmd[:2] == ['apk', 'add']]
                services = [cmd for cmd in commands if cmd[0] in ('firefox-vpn', 'rpcd')]
                self.assertEqual(bool(installs), success or scenario == 'install-failed')
                if success:
                    self.assertEqual(services, [['firefox-vpn', 'enable'], ['firefox-vpn', 'restart'], ['rpcd', 'restart']])
                    urls = [cmd[-1] for cmd in commands if cmd[0] == 'wget'][1:]
                    self.assertEqual(next(cmd[-1] for cmd in commands if cmd[0] == 'wget'),
                                     'https://api.github.com/repos/BercX/firefox-vpn-openwrt/releases/latest')
                    package_version = '0.2.0-r42' if scenario == 'new-revision' else '0.1.0-r13' if scenario == 'legacy-tag' else '0.1.0-r1'
                    tag = ('v' if scenario == 'legacy-tag' else '') + package_version
                    architecture = 'x86_64' if scenario == 'x86' else 'aarch64_cortex-a53'
                    self.assertEqual([url.rsplit('/', 1)[1] for url in urls], [
                        f'firefox-vpn-{package_version}-{architecture}.apk',
                        f'luci-app-firefox-vpn-{package_version}-all.apk',
                        'firefox-vpn-build-public.pem'])
                    self.assertTrue(all(f'/download/{tag}/' in url for url in urls))
                    self.assertEqual([Path(path).name for path in installs[0][2:]], [url.rsplit('/', 1)[1] for url in urls[:2]])
                    self.assertTrue((root / 'keys/firefox-vpn-build.pem').is_file())
                    verification = next(i for i, cmd in enumerate(commands) if cmd[0] == 'apk' and 'verify' in cmd)
                    self.assertEqual(commands[verification][-2:], installs[0][2:])
                    self.assertLess(verification, commands.index(installs[0]))
                else:
                    self.assertFalse(services)
                if not success and scenario != 'install-failed':
                    self.assertFalse((root / 'keys').exists())
                if scenario in ('not-root', 'bad-arch'):
                    self.assertFalse(any(cmd[0] == 'wget' for cmd in commands))
                if scenario in ('release-unavailable', 'no-release'):
                    self.assertIn('https://github.com/BercX/firefox-vpn-openwrt/releases', result.stderr)
                self.assertFalse(list(root.glob('download.*')), 'temporary files were not cleaned up')


if __name__ == '__main__':
    unittest.main()
