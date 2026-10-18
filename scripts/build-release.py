#!/usr/bin/env python3
"""Build versioned CLI archives and checksums. Does not publish releases."""
import argparse
import hashlib
import gzip
import io
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--output', type=Path, default=ROOT / 'dist')
    args = parser.parse_args()
    if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?', args.version):
        parser.error('version must be an explicit semantic release version')
    args.output.mkdir(parents=True, exist_ok=True)
    checksums = []
    for system, arch in [('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'amd64'), ('darwin', 'arm64')]:
        with tempfile.TemporaryDirectory(prefix='koala-release-') as directory:
            binary = Path(directory) / 'koala'
            env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED='0')
            subprocess.run(['go', 'build', '-mod=readonly', '-trimpath', '-ldflags',
                            '-s -w -X github.com/bingyuanng/koala/internal/cli.Version=' + args.version,
                            '-o', str(binary), './cmd/koala'], cwd=ROOT, env=env, check=True)
            archive = args.output / f'koala_{args.version}_{system}_{arch}.tar.gz'
            with archive.open('wb') as raw, gzip.GzipFile(filename='',mode='wb',fileobj=raw,mtime=0) as compressed, tarfile.open(fileobj=compressed,mode='w') as tar:
                data = binary.read_bytes()
                entry = tarfile.TarInfo('koala')
                entry.size, entry.mode, entry.mtime = len(data), 0o755, 0
                tar.addfile(entry, io.BytesIO(data))
            checksums.append(hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name)
            print(archive)
    (args.output / 'SHA256SUMS').write_text('\n'.join(checksums) + '\n')


if __name__ == '__main__':
    main()
