#!/usr/bin/env python3
"""Verify fixture generation without overwriting checked-in artifacts."""
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
FIXTURE = ROOT / 'examples/users'
BUF = ROOT / '.tools/bin/buf'


def run(*args):
    subprocess.run([str(BUF), *args], cwd=FIXTURE, check=True)


def snapshot(directory):
    return {str(path.relative_to(directory)): path.read_bytes()
            for path in directory.rglob('*') if path.is_file()}


def compare(expected, actual, description):
    differences = sorted(name for name in expected.keys() | actual.keys()
                         if expected.get(name) != actual.get(name))
    if differences:
        raise SystemExit(description + ':\n' + '\n'.join(differences))


if __name__ == '__main__':
    subprocess.run(['python3', str(ROOT / 'scripts/tools.py'), 'check-tools'], check=True)
    run('format', '--diff', '--exit-code')
    run('lint')
    with tempfile.TemporaryDirectory(prefix='koala-generation-') as directory:
        first, second = (Path(directory) / name for name in ('first', 'second'))
        run('generate', '--output', str(first))
        run('generate', '--output', str(second))
        generated = snapshot(first / 'gen')
        if not generated:
            raise SystemExit('Generation produced no artifacts')
        compare(generated, snapshot(second / 'gen'), 'Repeat generation differs')
        compare(generated, snapshot(FIXTURE / 'gen'), 'Checked-in artifacts are stale')
    subprocess.run(['go', 'build', '-mod=readonly', './examples/users/...'], cwd=ROOT, check=True)
    print('Buf lint/format and deterministic generation match checked-in artifacts; fixture builds')
