"""Native build grouping, ELF safety and OpenWrt package contract checks."""
import importlib.util
import contextlib
import io
import json
from pathlib import Path
import struct
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('build_local', ROOT / 'scripts/build_local.py')
local = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(local)
PUBLIC = b'fixture-public-key\n'


def elf(baseline, kind=1):
    cls, order, machine = local.ELF[baseline]
    endian = '<' if order == 1 else '>'
    data = bytearray(128)
    data[:6] = b'\x7fELF' + bytes((cls, order))
    struct.pack_into(endian + 'H', data, 18, machine)
    struct.pack_into(endian + ('I' if cls == 1 else 'Q'), data, 28 if cls == 1 else 32, 64)
    struct.pack_into(endian + 'HH', data, 42 if cls == 1 else 54, 32 if cls == 1 else 56, 1)
    struct.pack_into(endian + 'I', data, 64, kind)
    return data


class NativeBuildCheck(unittest.TestCase):
    def test_source_digest_ignores_local_docs(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(local, 'SOURCE', Path(temporary)):
            source = Path(temporary) / 'main.go'
            source.write_text('package main\n')
            original = local.source_digest()
            readme = Path(temporary) / 'README.md'
            readme.write_text('Local documentation\n')
            self.assertEqual(local.source_digest(), original)
            readme.unlink()
            self.assertEqual(local.source_digest(), original)
            source.write_text('package changed\n')
            self.assertNotEqual(local.source_digest(), original)

    def test_baselines_packaging_and_rejections(self):
        selected = local.select_architectures('all')
        self.assertEqual(len(selected), 30)
        self.assertEqual(set(selected.values()), set(local.BASELINES))
        for name in ('arm_fa526', 'armeb_xscale', '../bad', '', 'x86_64,x86_64'):
            with self.assertRaises(ValueError):
                local.select_architectures(name)
        builds, packages, verifications = [], [], []
        fail_verification = False

        def fake_run(command, **kwargs):
            nonlocal fail_verification
            args = [str(value) for value in command]
            if args[:2] == ['go', 'version']:
                return SimpleNamespace(stdout='test-go')
            if args[:2] == ['go', 'build']:
                env = kwargs['env']
                baseline = env['GOARCH']
                self.assertEqual(env['CGO_ENABLED'], '0')
                self.assertEqual(env['GOOS'], 'linux')
                self.assertEqual(env['GOENV'], 'off')
                self.assertEqual(env['GOEXPERIMENT'], 'nojsonv2')
                self.assertNotIn('GOFLAGS', env)
                self.assertIn(f'-ldflags=-s -w -X firefox-vpn/internal/version.Current={local.version()}', args)
                for key, value in local.BASELINES[baseline].items():
                    self.assertEqual(env[key], value)
                builds.append(baseline)
                Path(args[args.index('-o') + 1]).write_bytes(elf(baseline))
            elif args[:2] == ['openssl', 'pkey']:
                return SimpleNamespace(stdout=Path(args[args.index('-in') + 1]).read_bytes() if '-pubin' in args else PUBLIC)
            elif args[:3] == ['fakeroot', 'apk', 'mkpkg']:
                info = [args[index + 1] for index, value in enumerate(args) if value == '--info']
                packages.append(info)
                stage = Path(args[args.index('--files') + 1])
                name = next(value[5:] for value in info if value.startswith('name:'))
                for index, value in enumerate(args):
                    if value == '--script':
                        script = Path(args[index + 1].split(':', 1)[1]).read_text()
                        subprocess.run(['sh', '-n'], input=script, text=True, check=True)
                        self.assertIn('default_postinst' if 'post-' in args[index + 1] else 'default_prerm', script)
                if name == 'firefox-vpn':
                    arch = next(value[5:] for value in info if value.startswith('arch:'))
                    self.assertEqual((stage / 'usr/bin/firefox-vpn').read_bytes(), elf(selected[arch]))
                    self.assertTrue((stage / 'usr/libexec/rpcd/luci.firefox-vpn').is_symlink())
                    self.assertEqual((stage / 'etc/firefox-vpn/config.json').stat().st_mode & 0o777, 0o600)
                    self.assertEqual((stage / 'etc/firefox-vpn').stat().st_mode & 0o777, 0o700)
                    metadata = stage / 'lib/apk/packages'
                    self.assertEqual((stage / 'lib/upgrade/keep.d/firefox-vpn').read_text(), '/etc/firefox-vpn/\n')
                    self.assertIn('/lib/upgrade/keep.d/firefox-vpn', (metadata / f'{name}.list').read_text())
                    self.assertEqual((metadata / f'{name}.conffiles').read_text(), '/etc/firefox-vpn/\n/etc/firefox-vpn/config.json\n')
                    self.assertIn(local.digest(stage / 'etc/firefox-vpn/config.json'), (metadata / f'{name}.conffiles_static').read_text())
                else:
                    self.assertIn('arch:noarch', info)
                    self.assertTrue((stage / 'www/luci-static/resources/view/firefox-vpn/overview.js').is_file())
                    self.assertTrue((stage / 'usr/share/rpcd/acl.d/luci-app-firefox-vpn.json').is_file())
                    self.assertFalse(list(stage.rglob('*.svg')), 'OpenWrt packages must not include flag images')
                Path(args[args.index('--output') + 1]).write_bytes(b'fake-apk')
            elif 'verify' in args:
                verifications.append(args[-1])
                if fail_verification:
                    raise subprocess.CalledProcessError(1, args)
            else:
                self.fail(f'Unexpected command: {args}')
            return SimpleNamespace(stdout='')

        with tempfile.TemporaryDirectory() as temporary, patch.object(local, 'run', side_effect=fake_run), contextlib.redirect_stdout(io.StringIO()):
            root = Path(temporary)
            public = root / 'expected-public.pem'
            public.write_bytes(PUBLIC)
            binaries = root / 'binaries'
            with patch.dict('os.environ', {'GOFLAGS': '-race', 'GOARM': '7', 'GOAMD64': 'v4', 'GOEXPERIMENT': 'jsonv2'}):
                local.build(binaries, selected, 'go')
            self.assertEqual(len(builds), 10)
            self.assertCountEqual(builds, local.BASELINES)
            output = root / 'release'
            subset = local.select_architectures('aarch64_cortex-a53,aarch64_cortex-a76,x86_64')
            local.package(binaries, selected, output, 'apk', 'fakeroot', root / 'private.pem', public)
            self.assertEqual(len(packages), 31)
            self.assertEqual(len(verifications), 31)
            expected = {f'firefox-vpn-{local.version()}-{arch}.apk' for arch in selected}
            expected.add(f'luci-app-firefox-vpn-{local.version()}-all.apk')
            self.assertEqual({path.name for path in output.glob('*.apk')}, expected)
            self.assertEqual({path.name for path in output.iterdir()},
                             expected | {'firefox-vpn-build-public.pem', 'build-info.json'})
            manifest = json.loads((output / 'build-info.json').read_text())
            self.assertEqual(manifest['architectures'], selected)
            self.assertEqual(manifest['version'], local.version())
            self.assertEqual(manifest['goexperiment'], 'nojsonv2')
            self.assertEqual(set(manifest['packages']), expected)
            self.assertFalse((output / 'private.pem').exists())
            self.assertEqual((output / 'firefox-vpn-build-public.pem').read_bytes(), PUBLIC)
            saved_manifest = (binaries / 'manifest.json').read_text()
            for experiment in (None, 'jsonv2'):
                changed = json.loads(saved_manifest)
                if experiment is None:
                    del changed['goexperiment']
                else:
                    changed['goexperiment'] = experiment
                (binaries / 'manifest.json').write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    local.package(binaries, subset, root / 'wrong-experiment', 'apk', 'fakeroot', root / 'private.pem', public)
            (binaries / 'manifest.json').write_text(saved_manifest)
            with self.assertRaises(ValueError):
                local.package(binaries, subset, output, 'apk', 'fakeroot', root / 'private.pem', public)
            fail_verification = True
            with self.assertRaises(subprocess.CalledProcessError):
                local.package(binaries, subset, root / 'failed', 'apk', 'fakeroot', root / 'private.pem', public)
            self.assertFalse((root / 'failed').exists())
            with patch.object(local, 'source_digest', return_value='changed'):
                with self.assertRaises(ValueError):
                    local.package(binaries, subset, root / 'stale', 'apk', 'fakeroot', root / 'private.pem', public)
            with patch.object(local, 'version', return_value='0.1.0-r999'):
                with self.assertRaises(ValueError):
                    local.package(binaries, subset, root / 'stale-version', 'apk', 'fakeroot', root / 'private.pem', public)
            with patch.object(local, 'run', side_effect=[SimpleNamespace(stdout=b'wrong key'), SimpleNamespace(stdout=PUBLIC)]):
                with self.assertRaises(ValueError):
                    local.package(binaries, subset, root / 'wrong-key', 'apk', 'fakeroot', root / 'private.pem', public)
            (binaries / 'arm64').write_bytes(b'tampered')
            with self.assertRaises(ValueError):
                local.package(binaries, subset, root / 'tampered', 'apk', 'fakeroot', root / 'private.pem', public)
            for baseline in local.BASELINES:
                path = root / 'elf'
                path.write_bytes(elf(baseline))
                local.check_elf(path, baseline)
                for kind in (2, 3):
                    path.write_bytes(elf(baseline, kind))
                    with self.assertRaises(ValueError):
                        local.check_elf(path, baseline)


if __name__ == '__main__':
    unittest.main()
