"""Exercise signing boundaries with disposable keys; no real signing secret is read."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('sign_apks', ROOT / 'scripts/sign_apks.py')
signing = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(signing)
IMAGE = 'ghcr.io/openwrt/sdk:test@sha256:' + 'a' * 64


class SigningCheck(unittest.TestCase):
    def test_key_validation_and_cleanup(self):
        real_run = subprocess.run
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            private = root / 'disposable.pem'
            real_run(['openssl', 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256',
                      '-out', private], check=True, capture_output=True)
            key = private.read_text()
            public = real_run(['openssl', 'pkey', '-in', private, '-pubout'],
                              check=True, capture_output=True, text=True).stdout
            runner = root / 'runner'
            runner.mkdir()
            cases = [
                ('valid', key, public, IMAGE, None),
                ('crlf', key.replace('\n', '\r\n'), public, IMAGE, None),
                ('escaped', key.replace('\n', '\\n'), public.replace('\n', '\\n'), IMAGE, None),
                ('missing', '', public, IMAGE, ValueError),
                ('path', str(private), public, IMAGE, ValueError),
                ('public-not-private', public, public, IMAGE, ValueError),
                ('broken-private', '-----BEGIN PRIVATE KEY-----\nbroken\n-----END PRIVATE KEY-----', public, IMAGE, ValueError),
                ('missing-public', key, '', IMAGE, ValueError),
                ('broken-public', key, '-----BEGIN PUBLIC KEY-----\nbroken\n-----END PUBLIC KEY-----', IMAGE, ValueError),
                ('mutable-image', key, public, 'ghcr.io/openwrt/sdk:latest', ValueError),
                ('docker-failed', key, public, IMAGE, subprocess.CalledProcessError),
            ]
            other = root / 'other.pem'
            real_run(['openssl', 'genpkey', '-algorithm', 'EC', '-pkeyopt', 'ec_paramgen_curve:P-256',
                      '-out', other], check=True, capture_output=True)
            wrong_public = real_run(['openssl', 'pkey', '-in', other, '-pubout'],
                                    check=True, capture_output=True, text=True).stdout
            cases.append(('mismatch', key, wrong_public, IMAGE, ValueError))
            for name, secret, expected, image, error in cases:
                with self.subTest(case=name):
                    docker_calls = []

                    def run(command, **kwargs):
                        self.assertNotIn('APK_SIGNING_KEY', os.environ)
                        if command[0] != 'docker':
                            return real_run(command, **kwargs)
                        docker_calls.append(command)
                        mount = next(value for value in command if 'dst=/signing,' in value)
                        self.assertTrue(mount.endswith(',readonly'))
                        keys = Path(mount.split('src=', 1)[1].split(',dst=')[0])
                        self.assertEqual((keys / 'private-key.pem').stat().st_mode & 0o777, 0o600)
                        self.assertEqual(keys.stat().st_mode & 0o777, 0o700)
                        self.assertEqual((keys / 'firefox-vpn-build-public.pem').read_text(), public)
                        self.assertNotIn(secret, ' '.join(str(arg) for arg in command))
                        if name == 'docker-failed':
                            raise subprocess.CalledProcessError(1, ['docker'])
                        return SimpleNamespace(returncode=0)

                    with patch.dict(os.environ, {'APK_SIGNING_KEY': secret, 'APK_SIGNING_PUBLIC_KEY': expected,
                                                 'RUNNER_TEMP': str(runner)}), patch.object(signing.subprocess, 'run', side_effect=run):
                        if error:
                            with self.assertRaises(error):
                                signing.sign(root, root / name / 'release', image)
                        else:
                            signing.sign(root, root / name / 'release', image)
                    self.assertEqual(len(docker_calls), int(error is None or name == 'docker-failed'))
                    self.assertFalse(list(runner.iterdir()), 'temporary keys must be deleted on success and failure')


if __name__ == '__main__':
    unittest.main()
