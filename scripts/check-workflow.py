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
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
BUF = shutil.which('buf') or 'buf'


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
    http_address = grpc_address = None
    started = False
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
            text = line.strip()
            if text == 'Server started':
                started = True
            elif text.startswith('HTTP:'):
                http_address = text.removeprefix('HTTP:').strip()
            elif text.startswith('gRPC:'):
                grpc_address = text.removeprefix('gRPC:').strip()
            elif text == 'Press Ctrl+C to stop.' and started:
                assert http_address and grpc_address, history
                yield http_address, grpc_address
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
        run([cli, 'new', 'consumer', '--module', 'example.com/consumer', '--framework-path', ROOT], temporary)
        project = temporary / 'consumer'
        run(['mise', 'trust'], project)
        run([cli, 'new', 'consumer', '--framework-path', ROOT], temporary, expected=1)
        generation_args=['--buf',BUF]
        if args.local_plugins:
            generation_args += ['--local-plugins',args.local_plugins.resolve()]
        # A fresh project includes an application-owned health contract and implementation.
        health_paths = ('proto/health/v1/health.proto', 'internal/service/health/service.go')
        health_sources = {name: (project / name).read_bytes() for name in health_paths}
        configuration = json.loads((project / 'config.dev.json').read_text())
        configuration['server']['http']['port'] = 0
        configuration['server']['grpc']['port'] = 0
        configuration['telemetry']['traces']['enabled'] = False
        configuration['logging']['level'] = 'error'
        (project / 'config.dev.json').write_text(json.dumps(configuration))
        run([cli, 'gen', *generation_args], project)
        health_only = digest(project)
        run([cli, 'gen', '--strict', *generation_args], project)
        assert digest(project) == health_only
        assert {name: (project / name).read_bytes() for name in health_paths} == health_sources
        embedding = project / 'gen/openapi/openapi_gen.go'
        embedding_source = embedding.read_bytes()
        assert b'//go:embed api.swagger.json' in embedding_source
        registry_text = (project / 'internal/bootstrap/services_gen.go').read_text()
        assert 'var OpenAPI = openapi.Document' in registry_text
        assert 'HealthService_Health' not in registry_text
        assert 'gen/openapi/openapi_gen.go' in json.loads((project / '.koala/generation.json').read_text())['outputs']
        # The embedding source follows the same freshness and ownership rules as other outputs.
        embedding.write_bytes(embedding_source + b'\n// modified locally\n')
        assert 'generated output gen/openapi/openapi_gen.go' in run([cli, 'start'], project, expected=1)
        assert 'modified generated output gen/openapi/openapi_gen.go' in run([cli, 'gen', *generation_args], project, expected=1)
        embedding.write_bytes(embedding_source)
        # A failed bind must never print a successful startup message.
        with socket.socket() as occupied:
            occupied.bind(('127.0.0.1', 0))
            occupied.listen()
            configuration['server']['grpc']['port'] = occupied.getsockname()[1]
            (project / 'config.dev.json').write_text(json.dumps(configuration))
            assert 'Server started' not in run([cli, 'start'], project, expected=1)
        configuration['server']['grpc']['port'] = 0
        (project / 'config.dev.json').write_text(json.dumps(configuration))
        with server(cli, project) as (base, grpc):
            assert json.loads(request(base, '/healthz')) == {'status': 'ok'}
            assert json.loads(request(base, '/readyz')) == {'status': 'ok'}
            request(base, '/livez', 404)
            request(base, '/docs')
            assert {'/healthz', '/readyz'} <= json.loads(request(base, '/openapi.json'))['paths'].keys()
            # The running binary serves embedded bytes without reading the JSON from disk.
            spec_file = project / 'gen/openapi/api.swagger.json'
            spec_bytes = spec_file.read_bytes()
            spec_file.unlink()
            try:
                assert request(base, '/openapi.json') == spec_bytes
            finally:
                spec_file.write_bytes(spec_bytes)
            probe = project / 'cmd/health-probe/main.go'
            probe.parent.mkdir()
            probe.write_text('''package main
import("context";"os";"fmt";"time";pb "example.com/consumer/gen/go/health/v1";"google.golang.org/grpc";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();r,e:=pb.NewHealthServiceClient(c).Health(ctx,&pb.HealthRequest{});if e!=nil{panic(e)};ready,e:=pb.NewHealthServiceClient(c).Ready(ctx,&pb.ReadyRequest{});if e!=nil{panic(e)};if ready.Status!="ok"{panic(ready.Status)};fmt.Print(r.Status)}
''')
            assert run(['go', 'run', '-mod=readonly', './cmd/health-probe', grpc], project) == 'ok'
            shutil.rmtree(probe.parent)
        health_implementation = project / 'internal/service/health/service.go'
        health_implementation.write_text(health_sources['internal/service/health/service.go'].decode().replace('Status: "ok"', 'Status: "custom"'))
        with server(cli, project) as (base, grpc):
            assert json.loads(request(base, '/healthz')) == {'status': 'custom'}
        assert digest(project) == health_only
        health_implementation.write_bytes(health_sources['internal/service/health/service.go'])
        # Application startup owns cleanup throughout, including factory and Run failures.
        main_source = project / 'cmd/server/main.go'
        original_main = main_source.read_text()
        # Application-owned readiness drives both the HTTP and gRPC responses.
        assert 'IsReady: app.IsReady' in original_main
        main_source.write_text(original_main.replace('IsReady: app.IsReady', 'IsReady: func() bool { return false }'))
        with server(cli, project) as (base, grpc):
            assert json.loads(request(base, '/healthz')) == {'status': 'ok'}
            assert json.loads(request(base, '/readyz', 503))['code'] == 14
            probe = project / 'cmd/ready-probe/main.go'
            probe.parent.mkdir()
            probe.write_text('''package main
import("context";"os";"time";pb "example.com/consumer/gen/go/health/v1";"google.golang.org/grpc";"google.golang.org/grpc/codes";"google.golang.org/grpc/status";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();_,e=pb.NewHealthServiceClient(c).Ready(ctx,&pb.ReadyRequest{});if status.Code(e)!=codes.Unavailable{panic(e)}}
''')
            run(['go', 'run', '-mod=readonly', './cmd/ready-probe', grpc], project)
            shutil.rmtree(probe.parent)
        main_source.write_text(original_main)
        cleanup_registration = '''
func(context.Context) error {
 f, err := os.OpenFile("cleanup-marker", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
 if err != nil { return err }
 _, err = f.WriteString("closed\\n")
 return errors.Join(err, f.Close())
}
'''
        anchor = 'koala.Close(cfg.Server.ShutdownTimeout, telemetry.Shutdown)'
        assert anchor in original_main
        main_source.write_text(original_main.replace(anchor, 'koala.Close(cfg.Server.ShutdownTimeout, telemetry.Shutdown, ' + cleanup_registration.strip() + ')'))
        health_implementation.write_text(health_sources['internal/service/health/service.go'].decode().replace('return &Service{isReady: d.IsReady}, nil', 'return nil, nil'))
        failure = run([cli, 'start'], project, expected=1)
        assert 'factory returned nil implementation' in failure and 'Server started' not in failure, failure
        marker = project / 'cleanup-marker'
        assert marker.read_text() == 'closed\n'
        marker.unlink()
        health_implementation.write_bytes(health_sources['internal/service/health/service.go'])
        with server(cli, project) as (base, grpc):
            request(base, '/healthz')
        assert marker.read_text() == 'closed\n'
        marker.unlink()
        main_source.write_text(original_main)
        # Applications can explicitly opt into probes with their chosen paths.
        probe_registration = '''
if err := app.HandleHTTP("GET /alive", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })); err != nil { return err }
if err := app.HandleHTTP("GET /available", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { if !app.IsReady() { w.WriteHeader(http.StatusServiceUnavailable); return }; w.WriteHeader(http.StatusOK) })); err != nil { return err }
'''
        registration_anchor = 'if err := bootstrap.RegisterAll(app, dependencies); err != nil {'
        assert registration_anchor in original_main
        renamed_main = original_main.replace('import (', 'import (\n\"net/http\"').replace(registration_anchor, probe_registration + registration_anchor)
        main_source.write_text(renamed_main)
        with server(cli, project) as (base, grpc):
            assert json.loads(request(base, '/readyz')) == {'status': 'ok'}
            request(base, '/livez', 404)
            request(base, '/available')
            request(base, '/alive')
            assert json.loads(request(base, '/healthz')) == {'status': 'ok'}
        main_source.write_text(original_main)
        # Removing the sample leaves no implicit health endpoints.
        shutil.rmtree(project / 'proto')
        shutil.rmtree(project / 'internal/service')
        run([cli, 'gen'], project)
        assert not embedding.exists()
        assert not (project / 'gen/openapi/api.swagger.json').exists()
        with server(cli, project) as (base, grpc):
            request(base, '/healthz', 404)
            request(base, '/readyz', 404)
            request(base, '/livez', 404)
        for name, content in health_sources.items():
            path = project / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
        run([cli, 'gen', *generation_args], project)
        assert digest(project) == health_only
        run([cli, 'new', 'service', 'userService'], project)
        assert 'koala gen' in run([cli, 'start'], project, expected=1)
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
        output = run([cli, 'config', 'env', 'dev'], nested, env=env)
        assert 'never-export-this-secret' not in output and 'KOALA_ENV=dev' in output
        # The inverse conversion uses only KOALA_ variables, without reading project files.
        environment_only = {key: value for key, value in os.environ.items() if not key.startswith('KOALA_')}
        environment_only.update(KOALA_APP__ENVIRONMENT='prod', KOALA_SERVER__HTTP__PORT='8123', KOALA_ENV='prod')
        converted = json.loads(run([cli, 'config', 'env', 'json'], temporary, env=environment_only))
        assert converted == {'app': {'environment': 'prod'}, 'server': {'http': {'port': 8123}}, 'env': 'prod'}
        # Source the converted JSON values and prove they load through the selected environment.
        dotenv = temporary / 'config.env'
        dotenv.write_text(output)
        run([cli, 'config', 'json', '--output', 'config.example.json'], project)
        run([cli, 'config', 'json', '--output', 'config.example.json'], project, expected=1)
        run([cli, 'config', 'json', '--output', 'config.example.json', '--force'], project)
        run([cli, 'gen', *generation_args], nested)
        assert {name: (project / name).read_bytes() for name in health_paths} == health_sources
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
            request(base, '/healthz')
            request(base, '/v1/user/first', 501)
            request(base, '/v1/order/first', 501)
            spec = json.loads(request(base, '/openapi.json'))
            assert set(spec['paths']) == {'/healthz', '/readyz', '/v1/user/{id}', '/v1/order/{id}'}, spec['paths'].keys()
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
