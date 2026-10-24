#!/usr/bin/env python3
"""Cross-compile static baselines once, then create signed OpenWrt APK v3 files."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'firefox-vpn/src'
TAGS = 'netgo,osusergo,http2legacy'
GOEXPERIMENT = 'nojsonv2'
# Compatibility baselines, not CPU-tuned builds. Hardware testing is still required.
BASELINES = {
    'arm64': {'GOARM64': 'v8.0'},
    'arm': {'GOARM': '5,softfloat'},
    '386': {'GO386': 'softfloat'},
    'amd64': {'GOAMD64': 'v1'},
    'mips': {'GOMIPS': 'softfloat'},
    'mipsle': {'GOMIPS': 'softfloat'},
    'mips64': {'GOMIPS64': 'softfloat'},
    'mips64le': {'GOMIPS64': 'softfloat'},
    'riscv64': {'GORISCV64': 'rva20u64'},
    'loong64': {},
}
ARCHITECTURES = {
    **dict.fromkeys(('aarch64_cortex-a53', 'aarch64_cortex-a72',
                     'aarch64_cortex-a76', 'aarch64_generic'), 'arm64'),
    **dict.fromkeys(('arm_arm1176jzf-s_vfp', 'arm_arm926ej-s',
                     'arm_cortex-a15_neon-vfpv4', 'arm_cortex-a5_vfpv4',
                     'arm_cortex-a7', 'arm_cortex-a7_neon-vfpv4', 'arm_cortex-a7_vfpv4',
                     'arm_cortex-a8_vfpv3', 'arm_cortex-a9', 'arm_cortex-a9_neon',
                     'arm_cortex-a9_vfpv3-d16', 'arm_xscale'), 'arm'),
    **dict.fromkeys(('i386_pentium-mmx', 'i386_pentium4'), '386'),
    'x86_64': 'amd64',
    **dict.fromkeys(('mips_24kc', 'mips_mips32'), 'mips'),
    **dict.fromkeys(('mipsel_24kc', 'mipsel_24kc_24kf', 'mipsel_74kc', 'mipsel_mips32'), 'mipsle'),
    **dict.fromkeys(('mips64_mips64r2', 'mips64_octeonplus'), 'mips64'),
    'mips64el_mips64r2': 'mips64le',
    'riscv64_generic': 'riscv64',
    'loongarch64_generic': 'loong64',
}
# ELF class, byte order, machine. No target execution or cross-binutils needed.
ELF = {'arm64': (2, 1, 183), 'arm': (1, 1, 40), '386': (1, 1, 3),
       'amd64': (2, 1, 62), 'mips': (1, 2, 8), 'mipsle': (1, 1, 8),
       'mips64': (2, 2, 8), 'mips64le': (2, 1, 8),
       'riscv64': (2, 1, 243), 'loong64': (2, 1, 258)}


def run(command, **kwargs):
    return subprocess.run([str(arg) for arg in command], check=True, **kwargs)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_digest():
    result = hashlib.sha256()
    for path in sorted(SOURCE.rglob('*')):
        if path.is_file() and path.suffix != '.md':
            result.update(path.relative_to(SOURCE).as_posix().encode() + b'\0')
            result.update(path.read_bytes())
    return result.hexdigest()


def version():
    versions = []
    for package in ('firefox-vpn', 'luci-app-firefox-vpn'):
        text = (ROOT / package / 'Makefile').read_text()
        number = re.search(r'^PKG_VERSION:=(\S+)$', text, re.M).group(1)
        revision = re.search(r'^PKG_RELEASE:=(\d+)$', text, re.M).group(1)
        versions.append(f'{number}-r{revision}')
    if versions[0] != versions[1] or not re.fullmatch(r'[0-9][a-zA-Z0-9._-]*-r[0-9]+', versions[0]):
        raise ValueError('Package versions/revisions must match and form a safe release filename')
    return versions[0]


def select_architectures(text):
    selected = list(ARCHITECTURES) if text == 'all' else text.split(',')
    if not selected or len(set(selected)) != len(selected) or any(a not in ARCHITECTURES for a in selected):
        raise ValueError('Unknown, duplicate or unsupported package architecture; use list to see candidates')
    return {arch: ARCHITECTURES[arch] for arch in selected}


def check_elf(path, baseline):
    data = path.read_bytes()
    cls, order, machine = ELF[baseline]
    endian = '<' if order == 1 else '>'
    if len(data) < 64 or data[:6] != b'\x7fELF' + bytes((cls, order)):
        raise ValueError(f'Invalid ELF class/endianness: {path}')
    if struct.unpack_from(endian + 'H', data, 18)[0] != machine:
        raise ValueError(f'Wrong ELF machine: {path}')
    phoff = struct.unpack_from(endian + ('I' if cls == 1 else 'Q'), data, 28 if cls == 1 else 32)[0]
    entsize, count = struct.unpack_from(endian + 'HH', data, 42 if cls == 1 else 54)
    minimum = 32 if cls == 1 else 56
    if not count or entsize < minimum or phoff + entsize * count > len(data):
        raise ValueError(f'Invalid ELF program headers: {path}')
    for index in range(count):
        kind = struct.unpack_from(endian + 'I', data, phoff + index * entsize)[0]
        if kind in (2, 3):  # PT_DYNAMIC / PT_INTERP
            raise ValueError(f'Binary must be static, without a dynamic loader: {path}')


def build(directory, selected, go):
    directory.mkdir(parents=True, exist_ok=True)
    manifest = {'source': source_digest(), 'tags': TAGS, 'goexperiment': GOEXPERIMENT,
                'version': version(), 'baselines': {},
                'go': run([go, 'version'], capture_output=True, text=True).stdout.strip()}
    env = {key: value for key, value in os.environ.items()
           if key not in {'GOFLAGS', 'GOEXPERIMENT', 'GOLOONG64', 'GOWORK',
                          'GOARM', 'GOARM64', 'GO386', 'GOAMD64', 'GOMIPS', 'GOMIPS64', 'GORISCV64'}}
    env.update(CGO_ENABLED='0', GOOS='linux', GOENV='off', GOWORK='off', GOEXPERIMENT=GOEXPERIMENT)
    # ponytail: sequential builds cap peak RAM; parallelize only if measured necessary.
    with tempfile.TemporaryDirectory(dir=directory) as temporary:
        for baseline in dict.fromkeys(selected.values()):
            target = Path(temporary) / baseline
            started = time.monotonic()
            print(f'Building {baseline}: {BASELINES[baseline]}', flush=True)
            run([go, 'build', '-trimpath', '-tags', TAGS,
                 f'-ldflags=-s -w -X firefox-vpn/internal/version.Current={manifest["version"]}', '-o', target,
                 './cmd/firefox-vpn-openwrt'], cwd=SOURCE,
                env={**env, 'GOARCH': baseline, **BASELINES[baseline]})
            check_elf(target, baseline)
            manifest['baselines'][baseline] = {'sha256': digest(target), 'settings': BASELINES[baseline]}
            print(f'  {target.stat().st_size:,} bytes, {time.monotonic() - started:.1f}s', flush=True)
        if manifest['source'] != source_digest() or manifest['version'] != version():
            raise ValueError('Source or package version changed during compilation; rerun the build')
        for baseline in manifest['baselines']:
            os.replace(Path(temporary) / baseline, directory / baseline)
        target = Path(temporary) / 'manifest.json'
        target.write_text(json.dumps(manifest, indent=2) + '\n')
        os.replace(target, directory / 'manifest.json')


def copy_file(source, destination, mode=0o644):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
    destination.chmod(mode)


def stage(package, root, binary=None):
    root.mkdir(mode=0o755)
    if binary is not None:
        copy_file(binary, root / 'usr/bin/firefox-vpn', 0o755)
        link = root / 'usr/libexec/rpcd/luci.firefox-vpn'
        link.parent.mkdir(parents=True)
        link.symlink_to('/usr/bin/firefox-vpn')
        copy_file(ROOT / 'firefox-vpn/files/firefox-vpn.init', root / 'etc/init.d/firefox-vpn', 0o755)
        copy_file(ROOT / 'firefox-vpn/files/config.json', root / 'etc/firefox-vpn/config.json', 0o600)
        (root / 'etc/firefox-vpn').chmod(0o700)
        for source, name in [('files/LICENSE', 'LICENSE'), ('src/LICENSE', 'LICENSE.upstream')]:
            copy_file(ROOT / 'firefox-vpn' / source, root / 'usr/share/firefox-vpn' / name)
        copy_file(ROOT / 'README.md', root / 'usr/share/firefox-vpn/README.md')
        recipe = (ROOT / 'firefox-vpn/Makefile').read_text()
        conffiles = re.search(r'define Package/firefox-vpn/conffiles\n(.*?)\nendef', recipe, re.S).group(1)
        kept = [name for name in conffiles.splitlines() if not (root / name.lstrip('/')).is_file()]
        if kept:
            keep = root / 'lib/upgrade/keep.d' / package
            keep.parent.mkdir(parents=True)
            keep.write_text('\n'.join(kept) + '\n')
    else:
        for folder, destination in [('root', ''), ('htdocs', 'www')]:
            source = ROOT / package / folder
            for file in source.rglob('*'):
                if file.is_file():
                    copy_file(file, root / destination / file.relative_to(source))
    metadata = root / 'lib/apk/packages'
    metadata.mkdir(parents=True, exist_ok=True)
    files = sorted('/' + path.relative_to(root).as_posix() for path in root.rglob('*')
                   if path.is_file() or path.is_symlink())
    (metadata / f'{package}.list').write_text('\n'.join(files) + '\n')
    if binary is not None:
        (metadata / f'{package}.conffiles').write_text(conffiles + '\n')
        checksums = [f'{name} {digest(root / name.lstrip("/"))}' for name in conffiles.splitlines()
                     if (root / name.lstrip('/')).is_file()]
        (metadata / f'{package}.conffiles_static').write_text('\n'.join(checksums) + '\n')


def lifecycle(package):
    # Match OpenWrt package-pack.mk's APK hooks, including its upgrade marker.
    common = ('[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0\n'
              '. ${IPKG_INSTROOT}/lib/functions.sh\n'
              'export root="${IPKG_INSTROOT}"\n' + f'export pkgname="{package}"\n')
    post = '[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0\n' + common + 'add_group_and_user\ndefault_postinst\n'
    if package == 'luci-app-firefox-vpn':
        post += ('[ -n "${IPKG_INSTROOT}" ] || {\n'
                 '  rm -f /tmp/luci-indexcache.*\n  rm -rf /tmp/luci-modulecache/\n'
                 '  /etc/init.d/rpcd reload 2>/dev/null\n  exit 0\n}\n')
    return {'post-install': '#!/bin/sh\n' + post,
            'post-upgrade': '#!/bin/sh\nexport PKG_UPGRADE=1\n' + post,
            'pre-deinstall': '#!/bin/sh\n' + common + 'default_prerm\n'}


def package(directory, selected, output, apk, fakeroot, signing_key, public_key):
    release = version()
    derived = run(['openssl', 'pkey', '-in', signing_key, '-passin', 'pass:', '-pubout'], capture_output=True).stdout
    expected = run(['openssl', 'pkey', '-pubin', '-in', public_key, '-pubout'], capture_output=True).stdout
    if derived != expected:
        raise ValueError('Signing key does not match --public-key')
    manifest = json.loads((directory / 'manifest.json').read_text())
    if (manifest['source'] != source_digest() or manifest['tags'] != TAGS or
            manifest.get('goexperiment') != GOEXPERIMENT or manifest.get('version') != release):
        raise ValueError('Native binaries are stale; rebuild them before packaging')
    for baseline in set(selected.values()):
        entry = manifest['baselines'].get(baseline, {})
        if entry.get('settings') != BASELINES[baseline] or entry.get('sha256') != digest(directory / baseline):
            raise ValueError(f'Missing or modified baseline: {baseline}; rebuild it')
        check_elf(directory / baseline, baseline)
    if output.exists():
        raise ValueError(f'Output already exists; select a new --output directory: {output}')
    output.parent.mkdir(parents=True, exist_ok=True)
    # Publish nothing until the entire requested set has verified successfully.
    with tempfile.TemporaryDirectory(dir=output.parent) as temporary:
        work = Path(temporary)
        published = work / 'release'
        published.mkdir()
        keys = work / 'keys'
        keys.mkdir()
        public = keys / 'firefox-vpn-build-public.pem'
        public.write_bytes(expected)
        items = [('firefox-vpn', arch, directory / baseline) for arch, baseline in selected.items()]
        items.append(('luci-app-firefox-vpn', 'all', None))
        for name, arch, binary in items:
            root = work / f'{name}-{arch}'
            stage(name, root, binary)
            filename = f'{name}-{release}-{arch}.apk'
            command = [apk, 'mkpkg', '--info', f'name:{name}', '--info', f'version:{release}',
                       '--info', f'arch:{"noarch" if arch == "all" else arch}', '--info', 'license:MIT',
                       '--info', f'description:{"Firefox VPN proxy" if binary else "LuCI support for Firefox VPN"}',
                       '--info', f'origin:{name}', '--info', f'provides:{name}-any',
                       '--info', f'tags:openwrt:section={"net" if binary else "luci"}',
                       '--info', f'depends:{"ca-bundle libc rpcd" if binary else "firefox-vpn libc luci-base"}',
                       '--info', 'url:https://github.com/BercX/firefox-vpn-openwrt',
                       '--files', root, '--sign-key', signing_key, '--output', published / filename]
            for kind, script in lifecycle(name).items():
                script_path = work / f'{name}.{kind}'
                script_path.write_text(script)
                command += ['--script', f'{kind}:{script_path}']
            run([fakeroot, *command], env={**os.environ, 'SOURCE_DATE_EPOCH': '0'})
            run([apk, '--keys-dir', keys, 'verify', published / filename])
            print(f'Verified {filename}', flush=True)
        copy_file(public, published / public.name)
        manifest['architectures'] = selected
        manifest['version'] = release
        manifest['packages'] = {path.name: digest(path) for path in published.glob('*.apk')}
        (published / 'build-info.json').write_text(json.dumps(manifest, indent=2) + '\n')
        os.replace(published, output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('list', 'build', 'package'))
    parser.add_argument('--architectures', default='all', help='Comma-separated OpenWrt labels, or all')
    parser.add_argument('--binaries', type=Path, default=ROOT / '.build/native')
    parser.add_argument('--output', type=Path)
    parser.add_argument('--go', default=os.environ.get('GO', 'go'))
    parser.add_argument('--apk', default='apk', help='OpenWrt APK v3 host executable')
    parser.add_argument('--fakeroot', default='fakeroot', help='Ensure APK payload ownership is root:root')
    parser.add_argument('--signing-key', type=Path)
    parser.add_argument('--public-key', type=Path, help='Expected public PEM, also included with release downloads')
    args = parser.parse_args()
    try:
        selected = select_architectures(args.architectures)
        if args.action == 'list':
            for arch, baseline in selected.items():
                print(f'{arch}: {baseline} {BASELINES[baseline]}')
            print(f'{len(selected)} candidate APK architectures; {len(set(selected.values()))} Go builds; 1 LuCI APK')
        elif args.action == 'build':
            build(args.binaries.resolve(), selected, args.go)
        else:
            if not args.signing_key or not args.public_key:
                parser.error('package requires --signing-key and its matching --public-key')
            package(args.binaries.resolve(), selected,
                    (args.output or ROOT / 'dist/native' / f'v{version()}').resolve(),
                    args.apk, args.fakeroot, args.signing_key.resolve(), args.public_key.resolve())
    except (ValueError, OSError, subprocess.CalledProcessError, KeyError) as error:
        parser.exit(1, f'Error: {error}\n')


if __name__ == '__main__':
    os.umask(0o022)
    main()
