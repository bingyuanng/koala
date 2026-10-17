#!/usr/bin/env python3
"""Verify fixture generation without overwriting checked-in artifacts."""
import argparse
import json
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
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--local-plugins', type=Path, help='Explicit pinned plugin directory; default uses Buf remote execution')
    args = parser.parse_args()
    subprocess.run(['python3', str(ROOT / 'scripts/tools.py'), 'check-tools'], check=True)
    run('format', '--diff', '--exit-code')
    run('lint')
    with tempfile.TemporaryDirectory(prefix='koala-generation-') as directory:
        first, second = (Path(directory) / name for name in ('first', 'second'))
        template_args = []
        if args.local_plugins:
            template = (FIXTURE / 'buf.gen.yaml').read_text()
            contract = json.loads((ROOT / 'tools/versions.json').read_text())
            binaries = [('protocolbuffers/go','protoc-gen-go','google.golang.org/protobuf'),
                        ('grpc/go','protoc-gen-go-grpc','google.golang.org/grpc/cmd/protoc-gen-go-grpc'),
                        ('grpc-ecosystem/gateway','protoc-gen-grpc-gateway','github.com/grpc-ecosystem/grpc-gateway/v2'),
                        ('grpc-ecosystem/openapiv2','protoc-gen-openapiv2','github.com/grpc-ecosystem/grpc-gateway/v2')]
            for remote, binary, module in binaries:
                version=contract['plugins']['buf.build/'+remote]
                plugin=args.local_plugins.resolve() / binary
                metadata=subprocess.check_output(['go','version','-m',str(plugin)], text=True)
                if '\tmod\t'+module+'\t'+version+'\t' not in metadata:
                    raise SystemExit(binary + ' does not match its pinned module version')
                template=template.replace('remote: buf.build/'+remote+':'+version, 'local: '+str(plugin))
            template=template.replace('    out: gen/openapi','    strategy: all\n    out: gen/openapi')
            local_template=Path(directory) / 'buf.local.yaml'
            local_template.write_text(template)
            template_args=['--template',str(local_template)]
        run('generate', '--output', str(first), *template_args)
        run('generate', '--output', str(second), *template_args)
        generated = snapshot(first / 'gen')
        if not generated:
            raise SystemExit('Generation produced no artifacts')
        compare(generated, snapshot(second / 'gen'), 'Repeat generation differs')
        compare(generated, snapshot(FIXTURE / 'gen'), 'Checked-in artifacts are stale')
    subprocess.run(['go', 'build', '-mod=readonly', './examples/users/...'], cwd=ROOT, check=True)
    print('Buf lint/format and deterministic generation match checked-in artifacts; fixture builds')
