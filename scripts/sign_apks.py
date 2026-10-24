"""Validate environment-provided PEM keys and package APKs in the pinned SDK."""
import argparse
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
KEY_ERROR = ('APK_SIGNING_KEY must contain a complete, unencrypted private PEM key, '
             'not a filename, public/SSH key or base64-encoded file. '
             'Upload the file directly with gh secret set APK_SIGNING_KEY --env signing --repo BercX/firefox-vpn-openwrt < private-key.pem.')


def normalize(text):
    # Accept PEM pasted with Windows line endings or literal escaped newlines.
    return text.replace('\\r\\n', '\n').replace('\\n', '\n').replace('\r\n', '\n').strip() + '\n'


def sign(binaries, output, image):
    # Do not pass the secret environment to OpenSSL or the Docker client.
    private = normalize(os.environ.pop('APK_SIGNING_KEY', ''))
    public = normalize(os.environ.pop('APK_SIGNING_PUBLIC_KEY', ''))
    if not re.match(r'\A-----BEGIN (?:EC |RSA )?PRIVATE KEY-----\n', private):
        raise ValueError(KEY_ERROR)
    if not public.startswith('-----BEGIN PUBLIC KEY-----\n'):
        raise ValueError('Set APK_SIGNING_PUBLIC_KEY in the signing environment variables to the complete public PEM.')
    if not re.fullmatch(r'ghcr\.io/openwrt/sdk:[\w.-]+@sha256:[0-9a-f]{64}', image):
        raise ValueError('SDK image must be an official OpenWrt SDK pinned by SHA-256 digest')
    with tempfile.TemporaryDirectory(prefix='firefox-vpn-signing-', dir=os.environ.get('RUNNER_TEMP')) as temporary:
        keys = Path(temporary)
        key = keys / 'private-key.pem'
        key.write_text(private)
        key.chmod(0o600)
        expected = keys / 'firefox-vpn-build-public.pem'
        expected.write_text(public)
        try:
            subprocess.run(['openssl', 'pkey', '-in', key, '-passin', 'pass:', '-check', '-noout'],
                           check=True, capture_output=True)
            derived = subprocess.run(['openssl', 'pkey', '-in', key, '-passin', 'pass:', '-pubout'],
                                     check=True, capture_output=True).stdout
        except subprocess.CalledProcessError:
            raise ValueError(KEY_ERROR) from None
        try:
            canonical = subprocess.run(['openssl', 'pkey', '-pubin', '-in', expected, '-pubout'],
                                       check=True, capture_output=True).stdout
        except subprocess.CalledProcessError:
            raise ValueError('APK_SIGNING_PUBLIC_KEY is not a valid public PEM key') from None
        if derived != canonical:
            raise ValueError('APK_SIGNING_KEY does not match APK_SIGNING_PUBLIC_KEY; upload a matching pair')
        expected.write_bytes(canonical)
        output = output.resolve()
        output.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run([
            'docker', 'run', '--rm', '--init', '--user', 'root',
            '--mount', f'type=bind,src={ROOT},dst=/work,readonly',
            '--mount', f'type=bind,src={binaries.resolve()},dst=/binaries,readonly',
            '--mount', f'type=bind,src={keys},dst=/signing,readonly',
            '--mount', f'type=bind,src={output.parent},dst=/output',
            '--env', 'STAGING_DIR_HOST=/builder/staging_dir/host',
            '--entrypoint', 'python3', image, '/work/scripts/build_local.py', 'package',
            '--binaries', '/binaries', '--apk', '/builder/staging_dir/host/bin/apk',
            '--fakeroot', '/builder/staging_dir/host/bin/fakeroot',
            '--signing-key', '/signing/private-key.pem',
            '--public-key', '/signing/firefox-vpn-build-public.pem',
            '--output', f'/output/{output.name}',
        ], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binaries', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--sdk-image', required=True)
    args = parser.parse_args()
    try:
        sign(args.binaries, args.output, args.sdk_image)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'Error: {error}\n')


if __name__ == '__main__':
    main()
