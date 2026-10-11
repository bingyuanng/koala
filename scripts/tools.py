#!/usr/bin/env python3
"""Install/check the machine-readable tool contract. No credentials required."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
CONTRACT = json.loads((ROOT / 'tools/versions.json').read_text())

def check(require_buf=False):
    errors = []
    module = (ROOT / 'go.mod').read_text()
    if f"go {CONTRACT['go']}\n" not in module:
        errors.append('go.mod minimum Go version differs from tools/versions.json')
    output = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=ROOT, text=True)
    modules = []
    decoder = json.JSONDecoder()
    while output.strip():
        item, end = decoder.raw_decode(output.lstrip())
        modules.append(item)
        output = output.lstrip()[end:]
    actual = {m['Path']: m.get('Version') for m in modules}
    for name, version in CONTRACT['modules'].items():
        if actual.get(name) != version:
            errors.append(f'{name}: expected {version}, got {actual.get(name)}')
    plugins = (ROOT / 'tools/buf.gen.yaml').read_text()
    actual_plugins = [line.strip().removeprefix('- remote: ') for line in plugins.splitlines() if line.strip().startswith('- remote: ')]
    expected = [f'{name}:{version}' for name, version in CONTRACT['plugins'].items()]
    if sorted(actual_plugins) != sorted(expected):
        errors.append('remote plugin configuration differs from tools/versions.json')
    gateway = CONTRACT['modules']['github.com/grpc-ecosystem/grpc-gateway/v2']
    for name in ('gateway', 'openapiv2'):
        if CONTRACT['plugins'][f'buf.build/grpc-ecosystem/{name}'] != gateway:
            errors.append(f'{name} generator/runtime mismatch')
    if require_buf:
        try:
            version = subprocess.check_output([str(ROOT / '.tools/bin/buf'), '--version'], text=True).strip()
            if version != CONTRACT['buf']:
                errors.append(f"Buf: expected {CONTRACT['buf']}, got {version}")
        except (OSError, subprocess.CalledProcessError):
            errors.append('Buf is missing; run python3 scripts/tools.py install')
    if errors:
        raise SystemExit('\n'.join(errors))
    print('Version contract matches' + ('; pinned Buf verified' if require_buf else ''))

if __name__ == '__main__':
    mode = sys.argv[1] if len(sys.argv) == 2 else ''
    if mode == 'install':
        import os
        target = ROOT / '.tools/bin'
        target.mkdir(parents=True, exist_ok=True)
        env = dict(os.environ, GOBIN=str(target))
        subprocess.run(['go', 'install', 'github.com/bufbuild/buf/cmd/buf@v' + CONTRACT['buf']], cwd=ROOT, env=env, check=True)
        check(True)
    elif mode in ('check', 'check-tools'):
        check(mode == 'check-tools')
    else:
        raise SystemExit('usage: python3 scripts/tools.py install|check|check-tools')
