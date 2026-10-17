#!/usr/bin/env python3
"""Networked CLI acceptance checks using isolated consumer applications."""
from pathlib import Path
import argparse
import contextlib
import hashlib
import json
import os
import queue
import shutil
import signal
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
BUF = ROOT / '.tools/bin/buf'


def run(args, cwd, expected=0, env=None):
    result = subprocess.run([str(a) for a in args], cwd=cwd, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env)
    if (expected == 0 and result.returncode != 0) or (expected != 0 and result.returncode == 0):
        raise RuntimeError(f'{args} exited {result.returncode}:\n{result.stdout}')
    return result.stdout


def request(base, path, expected=200):
    try:
        response = urllib.request.urlopen(base + path, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        assert response.code == expected, (path, response.code, body)
        return body


@contextlib.contextmanager
def server(cli, project):
    process = subprocess.Popen([str(cli), 'start'], cwd=project, text=True,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    lines = queue.Queue()
    reader = threading.Thread(target=lambda: [lines.put(line) for line in process.stdout], daemon=True)
    reader.start()
    history = []
    try:
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                line = lines.get(timeout=0.1)
            except queue.Empty:
                if process.poll() is not None:
                    raise RuntimeError('start exited before readiness:\n' + ''.join(history))
                continue
            history.append(line)
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if value.get('msg') == 'application ready':
                yield 'http://' + value['http'], value['grpc']
                break
        else:
            raise RuntimeError('server startup timed out:\n' + ''.join(history))
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
        try:
            code = process.wait(timeout=20)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise RuntimeError('signal forwarding/shutdown timed out')
        process.stdout.close()
        assert code == 0, ('server exited', code, history)


def digest(project):
    return {str(p.relative_to(project)): hashlib.sha256(p.read_bytes()).hexdigest()
            for prefix in ('gen', 'internal/bootstrap', '.koala')
            for p in (project / prefix).rglob('*') if p.is_file()}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--local-plugins', type=Path, help='Directory containing the four exactly pinned protoc plugins; default uses Buf remote plugins')
    args=parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='koala-workflow-') as temporary:
        temporary = Path(temporary)
        cli = temporary / 'koala'
        run(['go', 'build', '-o', cli, './cmd/koala'], ROOT)
        run([cli, 'new', 'consumer', '--module', 'example.com/consumer'], temporary)
        project = temporary / 'consumer'
        run(['go', 'mod', 'edit', '-replace=github.com/bingyuanng/koala=' + str(ROOT)], project)
        run([cli, 'new', 'consumer'], temporary, expected=1)
        run([cli, 'new', 'service', 'userService'], project)
        run([cli, 'new', 'service', 'UserService'], project, expected=1)
        # Exercise the manually authored proto path: delete the scaffolded shell.
        shell = project / 'internal/service/user/service.go'
        shell.unlink()
        user_proto = project / 'proto/user/v1/user.proto'
        order_proto = project / 'proto/order/v1/order.proto'
        order_proto.parent.mkdir(parents=True)
        order_proto.write_text(user_proto.read_text().replace('UserService', 'OrderService').replace('user', 'order'))
        nested = project / 'cmd/server'
        env = dict(os.environ, KOALA_DATABASE__PASSWORD='never-export-this-secret')
        output = run([cli, 'config', 'env'], nested, env=env)
        assert 'never-export-this-secret' not in output and 'KOALA_ENV=cloud' in output
        # Source the quoted dotenv defaults and prove they load through cloud mode.
        dotenv = temporary / 'config.env'
        dotenv.write_text(output)
        run([cli, 'config', 'json', '--output', 'config.example.json'], project)
        run([cli, 'config', 'json', '--output', 'config.example.json'], project, expected=1)
        run([cli, 'config', 'json', '--output', 'config.example.json', '--force'], project)
        generation_args=['--buf',BUF]
        if args.local_plugins:
            generation_args += ['--local-plugins',args.local_plugins.resolve()]
        run([cli, 'gen', *generation_args], nested)
        assert shell.exists() and 'UnimplementedUserServiceServer' in shell.read_text()
        original_shell = shell.read_bytes()
        before = digest(project)
        run([cli, 'gen', *generation_args], project)
        assert digest(project) == before and shell.read_bytes() == original_shell
        run([cli, 'gen', '--strict', *generation_args], project, expected=1)
        configuration = json.loads((project / 'config.dev.json').read_text())
        configuration['server']['http']['port'] = 0
        configuration['server']['grpc']['port'] = 0
        configuration['telemetry']['traces']['enabled'] = False
        (project / 'config.dev.json').write_text(json.dumps(configuration))
        with server(cli, project) as (base, grpc):
            request(base, '/v1/user/first', 501)
            request(base, '/v1/order/first', 501)
            spec = json.loads(request(base, '/openapi.json'))
            assert len(spec['paths']) == 2, spec['paths'].keys()
            assert '/v1/user/{id}' in spec['paths'] and '/v1/order/{id}' in spec['paths']
            assert b'https://' not in request(base, '/docs')
        # Implement an existing method. Startup must rebuild without generation.
        source = shell.read_text().replace('import (', 'import (\n"context"')
        source += '''\nfunc(s *Service)Get(ctx context.Context,r *pb.GetRequest)(*pb.GetResponse,error){return &pb.GetResponse{Id:r.Id},nil}\n'''
        shell.write_text(source)
        run(['gofmt', '-w', shell], project)
        assert digest(project) == before
        probe = project / 'cmd/probe/main.go'
        probe.parent.mkdir()
        probe.write_text('''package main
import("context";"os";"fmt";"time";pb "example.com/consumer/gen/go/user/v1";"google.golang.org/grpc";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();r,e:=pb.NewUserServiceClient(c).Get(ctx,&pb.GetRequest{Id:"grpc-proof"});if e!=nil{panic(e)};fmt.Print(r.Id)}
''')
        run(['go', 'build', '-o', temporary / 'probe', './cmd/probe'], project)
        with server(cli, project) as (base, grpc):
            assert json.loads(request(base, '/v1/user/rebuilt'))['id'] == 'rebuilt'
            assert run([temporary / 'probe', grpc], project) == 'grpc-proof'
        user_proto.write_text(user_proto.read_text() + '\n// Changed contract input.\n')
        assert 'koala gen' in run([cli, 'start'], project, expected=1)
        run([cli, 'gen', *generation_args], project)
        assert shell.read_text() == source or 'func (s *Service) Get' in shell.read_text()
        registry = project / 'internal/bootstrap/services_gen.go'
        saved_registry = registry.read_bytes()
        saved_shell = shell.read_text()
        shell.write_text(saved_shell.replace('//koala:service user.v1.UserService', '//koala:service missing.v1.UnknownService'))
        run([cli, 'gen', *generation_args], project, expected=1)
        assert registry.read_bytes() == saved_registry
        shell.write_text(saved_shell)
        # Removing a service and its implementation removes only owned outputs.
        order_proto.unlink()
        shutil.rmtree(project / 'internal/service/order')
        run([cli, 'gen', *generation_args], project)
        assert not (project / 'gen/go/order/v1/order.pb.go').exists()
        assert 'OrderService' not in registry.read_text()
        run([cli, 'gen', '--strict', *generation_args], project)
        # A new RPC uses the existing embedding shell and gets a fallback until implemented.
        definition=user_proto.read_text()
        definition=definition.replace('}\n\n// GetRequest', '  // Extra is a newly added endpoint.\n  rpc Extra(ExtraRequest) returns (ExtraResponse) {\n    option (google.api.http) = {get: "/v1/user-extra/{id}"};\n  }\n}\n\n// GetRequest', 1)
        definition+='\n// ExtraRequest identifies the additional resource.\nmessage ExtraRequest {string id = 1;}\n// ExtraResponse contains its identifier.\nmessage ExtraResponse {string id = 1;}\n'
        user_proto.write_text(definition)
        run([cli,'gen',*generation_args],project)
        with server(cli,project) as (base,grpc):
            request(base,'/v1/user-extra/new',501)
            assert json.loads(request(base,'/v1/user/still-implemented'))['id']=='still-implemented'
        run([cli,'gen','--strict',*generation_args],project,expected=1)
        source=shell.read_text()+'\nfunc(s *Service)Extra(ctx context.Context,r *pb.ExtraRequest)(*pb.ExtraResponse,error){return &pb.ExtraResponse{Id:r.Id},nil}\n'
        shell.write_text(source)
        run(['gofmt','-w',shell],project)
        with server(cli,project) as (base,grpc):
            assert json.loads(request(base,'/v1/user-extra/implemented'))['id']=='implemented'
        run([cli,'gen','--strict',*generation_args],project)
        # Prove source-free production execution after building with Buf unavailable.
        run(['go', 'build', '-mod=readonly', '-o', temporary / 'server', './cmd/server'], project)
        run(['go', 'install', '-mod=readonly', './cmd/koala'], ROOT,
            env=dict(os.environ, GOBIN=str(temporary / 'installed')))
        assert (temporary / 'installed/koala').exists()
        print('CLI acceptance passed: manual proto, one-time shells, two services, both transports, inherited fallback, rebuild without generation, freshness, strict checks, ownership, docs, signal forwarding and installation.')


if __name__ == '__main__':
    main()
