"""Run the workflow's release and cleanup commands without changing GitHub."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]
MOCK = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with pathlib.Path(os.environ['GH_LOG']).open('a') as log:
    log.write(json.dumps(args) + '\n')
scenario = os.environ['SCENARIO']
if args[:3] == ['api', '--method', 'POST']:
    sys.exit(1 if scenario == 'tag-exists' else 0)
if args[:2] == ['release', 'create']:
    sys.exit(1 if scenario == 'upload-failed' else 0)
if args[:2] == ['release', 'edit']:
    sys.exit(1 if scenario == 'publish-failed' else 0)
if args[:2] == ['api', '--paginate']:
    if scenario == 'list-failed':
        sys.exit(1)
    assert args[-1] == '.artifacts[] | select(.name == "native-baselines") | .id'
    if scenario != 'empty':
        print('123\n456')
elif args[:3] != ['api', '--method', 'DELETE']:
    sys.exit(2)
'''


class ReleaseCheck(unittest.TestCase):
    def test_publish_and_cleanup(self):
        workflow = (ROOT / '.github/workflows/build.yml').read_text()
        publish = textwrap.dedent(re.search(r'      - name: Publish GitHub release\n.*?        run: \|\n(.*?)(?=\n  cleanup:)', workflow, re.S).group(1))
        cleanup = textwrap.dedent(workflow.split('        shell: bash\n        run: |\n', 1)[1])
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binaries = root / 'bin'
            binaries.mkdir()
            gh = binaries / 'gh'
            gh.write_text(MOCK)
            gh.chmod(0o755)
            release = root / 'firefox-vpn-output/release'
            release.mkdir(parents=True)
            (release / 'build-info.json').write_text(json.dumps({'version': '0.1.0-r1'}))
            for filename in ('firefox-vpn-0.1.0-r1-x86_64.apk', 'luci-app-firefox-vpn-0.1.0-r1-all.apk',
                             'firefox-vpn-build-public.pem'):
                (release / filename).write_text('fixture')
            log = root / 'gh.jsonl'
            env = {**os.environ, 'PATH': str(binaries) + ':' + os.environ['PATH'], 'RUNNER_TEMP': str(root),
                   'GITHUB_REPOSITORY': 'test/repo', 'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '99', 'GH_LOG': str(log)}
            for scenario in ('ok', 'tag-exists', 'upload-failed', 'publish-failed'):
                with self.subTest(publish=scenario):
                    log.write_text('')
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', publish], env={**env, 'SCENARIO': scenario}, capture_output=True, text=True)
                    calls = [json.loads(line) for line in log.read_text().splitlines()]
                    self.assertEqual(result.returncode == 0, scenario == 'ok', result.stderr)
                    self.assertEqual(calls[0], ['api', '--method', 'POST', 'repos/test/repo/git/refs',
                                              '-f', 'ref=refs/tags/0.1.0-r1', '-f', 'sha=' + 'a' * 40])
                    self.assertEqual(len(calls), 1 if scenario == 'tag-exists' else 2 if scenario == 'upload-failed' else 3)
                    if scenario != 'tag-exists':
                        self.assertIn('--draft', calls[1])
                        self.assertIn('--verify-tag', calls[1])
                        self.assertEqual(calls[1][2], '0.1.0-r1')
                        self.assertEqual(calls[1][calls[1].index('--title') + 1], calls[1][2])
                        self.assertNotIn('--prerelease', calls[1])
                        self.assertEqual(set(calls[1][3:calls[1].index('--repo')]), {str(path) for path in release.iterdir()})
                    if len(calls) == 3:
                        self.assertEqual(calls[2], ['release', 'edit', '0.1.0-r1', '--repo', 'test/repo', '--draft=false', '--latest'])
            for scenario in ('ok', 'empty', 'list-failed'):
                with self.subTest(cleanup=scenario):
                    log.write_text('')
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', cleanup], env={**env, 'SCENARIO': scenario}, capture_output=True, text=True)
                    calls = [json.loads(line) for line in log.read_text().splitlines()]
                    self.assertEqual(result.returncode == 0, scenario != 'list-failed', result.stderr)
                    self.assertEqual(calls[0][2], 'repos/test/repo/actions/runs/99/artifacts')
                    self.assertEqual(calls[1:], [['api', '--method', 'DELETE', f'repos/test/repo/actions/artifacts/{identifier}']
                                               for identifier in ('123', '456')] if scenario == 'ok' else [])


if __name__ == '__main__':
    unittest.main()
