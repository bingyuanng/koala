// Run networked CLI acceptance checks using an isolated consumer application.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	s "github.com/bingyuanng/koala/scripts/internal/scriptutil"
)

func main() { s.Main(checkWorkflow) }
func jsonObject(text string) map[string]any {
	var value map[string]any
	s.Must(json.Unmarshal([]byte(text), &value))
	return value
}
func writeJSON(path string, value any) {
	data, err := json.Marshal(value)
	s.Must(err)
	s.Write(path, string(data))
}
func request(base, path string, status ...int) string {
	expected := 200
	if len(status) > 0 {
		expected = status[0]
	}
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(base + path)
	s.Must(err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	s.Must(err)
	s.Check(response.StatusCode == expected, "%s returned %d, expected %d: %s", path, response.StatusCode, expected, body)
	return string(body)
}
func paths(spec string) map[string]any { return jsonObject(spec)["paths"].(map[string]any) }
func digest(project string) map[string]map[string]string {
	result := map[string]map[string]string{}
	for _, prefix := range []string{"gen", "internal/bootstrap", ".koala"} {
		result[prefix] = s.Snapshot(filepath.Join(project, prefix))
	}
	return result
}
func server(cli, project string, fn func(string, string)) {
	cmd := exec.Command(cli, "start")
	cmd.Dir = project
	pipe, err := cmd.StdoutPipe()
	s.Must(err)
	cmd.Stderr = cmd.Stdout
	s.Must(cmd.Start())
	lines := make(chan string, 128)
	readDone := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readDone <- scanner.Err()
	}()
	// Wait starts only after the output reader finishes, preserving the readiness lines.
	done := make(chan error, 1)
	go func() { readErr := <-readDone; done <- errors.Join(readErr, cmd.Wait()) }()
	draining := false
	drain := func() {
		if !draining {
			draining = true
			go func() {
				for range lines {
				}
			}()
		}
	}
	defer func() {
		drain()
		err := cmd.Process.Signal(syscall.SIGTERM)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			panic(err)
		}
		select {
		case err := <-done:
			s.Check(err == nil, "server shutdown failed: %v", err)
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			panic("signal forwarding/shutdown timed out")
		}
	}()
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	started := false
	base, grpc := "", ""
	history := []string{}
	for {
		select {
		case line, ok := <-lines:
			s.Check(ok, "start exited before readiness:\n%s", strings.Join(history, "\n"))
			history = append(history, line)
			line = strings.TrimSpace(line)
			switch {
			case line == "Server started":
				started = true
			case strings.HasPrefix(line, "HTTP:"):
				base = strings.TrimSpace(strings.TrimPrefix(line, "HTTP:"))
			case strings.HasPrefix(line, "gRPC:"):
				grpc = strings.TrimSpace(strings.TrimPrefix(line, "gRPC:"))
			case line == "Press Ctrl+C to stop." && started:
				s.Check(base != "" && grpc != "", "startup missing transport addresses: %v", history)
				// Keep draining output while the consumer assertions run.
				drain()
				fn(base, grpc)
				return
			}
		case <-timeout.C:
			panic("server startup timed out:\n" + strings.Join(history, "\n"))
		}
	}
}
func checkWorkflow() {
	flag.Parse()
	s.Check(flag.NArg() == 0, "unexpected arguments: %v", flag.Args())
	root := s.Root()
	temporary := s.Temp("koala-workflow-")
	defer os.RemoveAll(temporary)
	cli := filepath.Join(temporary, "koala")
	project := filepath.Join(temporary, "consumer")
	p := func(name string) string { return filepath.Join(project, name) }
	run := func(args ...string) string { return s.Run(project, nil, false, append([]string{cli}, args...)...) }
	fail := func(args ...string) string { return s.Run(project, nil, true, append([]string{cli}, args...)...) }
	serve := func(fn func(string, string)) { server(cli, project, fn) }
	s.Run(root, nil, false, "go", "build", "-o", cli, "./cmd/koala")
	s.Run(temporary, nil, false, cli, "new", "consumer", "--module", "example.com/consumer", "--framework-path", root)
	s.Run(project, nil, false, "mise", "trust")
	s.Run(temporary, nil, true, cli, "new", "consumer", "--framework-path", root)
	buf, err := exec.LookPath("buf")
	s.Must(err)
	generationArgs := []string{"--buf", buf}

	gen := func(strict, failure bool) string {
		args := []string{"gen"}
		if strict {
			args = append(args, "--strict")
		}
		args = append(args, generationArgs...)
		if failure {
			return fail(args...)
		}
		return run(args...)
	}
	healthPaths := []string{"proto/health/v1/health.proto", "internal/service/health/service.go"}
	healthSources := map[string]string{}
	for _, name := range healthPaths {
		healthSources[name] = s.Read(p(name))
	}
	checkHealthSources := func() {
		for _, name := range healthPaths {
			s.Compare(s.Read(p(name)), healthSources[name], "health source changed: "+name)
		}
	}
	configuration := jsonObject(s.Read(p("config.dev.json")))
	setPort := func(transport string, port int) {
		configuration["server"].(map[string]any)[transport].(map[string]any)["port"] = port
	}
	setPort("http", 0)
	setPort("grpc", 0)
	configuration["telemetry"].(map[string]any)["traces"].(map[string]any)["enabled"] = false
	configuration["logging"].(map[string]any)["level"] = "error"
	saveConfig := func() { writeJSON(p("config.dev.json"), configuration) }
	saveConfig()
	// New projects establish ownership before generation, including the placeholder registry.
	initialRegistry := p("internal/bootstrap/services_gen.go")
	s.Write(initialRegistry, s.Read(initialRegistry)+"\n// unsupported local edit\n")
	gen(false, false)
	s.NotContains(s.Read(initialRegistry), "unsupported local edit")
	healthOnly := digest(project)
	gen(true, false)
	s.Compare(digest(project), healthOnly, "strict generation changed outputs")
	checkHealthSources()
	embedding := p("gen/openapi/openapi_gen.go")
	embeddingSource := s.Read(embedding)
	s.Contains(embeddingSource, "//go:embed api.swagger.json")
	registry := p("internal/bootstrap/services_gen.go")
	registryText := s.Read(registry)
	s.Contains(registryText, "var OpenAPI = openapi.Document")
	s.NotContains(registryText, "HealthService_Health")
	outputs := jsonObject(s.Read(p(".koala/generation.json")))["outputs"].(map[string]any)
	_, ok := outputs["gen/openapi/openapi_gen.go"]
	s.Check(ok, "embedding missing from ownership manifest")
	s.Write(embedding, embeddingSource+"\n// modified locally\n")
	s.Contains(fail("start"), "generated output gen/openapi/openapi_gen.go")
	gen(false, false)
	s.Compare(s.Read(embedding), embeddingSource, "generation did not restore owned output")
	checkHealthSources()
	func() {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		s.Must(err)
		defer occupied.Close()
		setPort("grpc", occupied.Addr().(*net.TCPAddr).Port)
		saveConfig()
		s.NotContains(fail("start"), "Server started")
	}()
	setPort("grpc", 0)
	saveConfig()
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/healthz")), map[string]any{"status": "ok"}, "health response differs")
		s.Compare(jsonObject(request(base, "/readyz")), map[string]any{"status": "ok"}, "readiness response differs")
		request(base, "/livez", 404)
		request(base, "/docs")
		spec := paths(request(base, "/openapi.json"))
		for _, name := range []string{"/healthz", "/readyz"} {
			_, ok := spec[name]
			s.Check(ok, "missing OpenAPI path %s", name)
		}
		func() {
			path := p("gen/openapi/api.swagger.json")
			content := s.Read(path)
			s.Remove(path)
			defer s.Write(path, content)
			s.Compare(request(base, "/openapi.json"), content, "spec is not embedded")
		}()
		probe := p("cmd/health-probe/main.go")
		s.Write(probe, healthProbe)
		s.Compare(s.Run(project, nil, false, "go", "run", "-mod=readonly", "./cmd/health-probe", grpc), "ok", "gRPC health differs")
		s.Must(os.RemoveAll(filepath.Dir(probe)))
	})
	healthImplementation := p("internal/service/health/service.go")
	s.Write(healthImplementation, strings.ReplaceAll(healthSources[healthPaths[1]], `Status: "ok"`, `Status: "custom"`))
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/healthz")), map[string]any{"status": "custom"}, "application health override failed")
	})
	s.Compare(digest(project), healthOnly, "business implementation changed generated outputs")
	s.Write(healthImplementation, healthSources[healthPaths[1]])
	mainSource := p("cmd/server/main.go")
	originalMain := s.Read(mainSource)
	s.Contains(originalMain, "IsReady: app.IsReady")
	s.Write(mainSource, strings.ReplaceAll(originalMain, "IsReady: app.IsReady", "IsReady: func() bool { return false }"))
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/healthz")), map[string]any{"status": "ok"}, "health failed")
		s.Compare(jsonObject(request(base, "/readyz", 503))["code"], float64(14), "readiness code differs")
		probe := p("cmd/ready-probe/main.go")
		s.Write(probe, readyProbe)
		s.Run(project, nil, false, "go", "run", "-mod=readonly", "./cmd/ready-probe", grpc)
		s.Must(os.RemoveAll(filepath.Dir(probe)))
	})
	s.Write(mainSource, originalMain)
	anchor := "koala.Close(cfg.Server.ShutdownTimeout, telemetry.Shutdown)"
	s.Contains(originalMain, anchor)
	s.Write(mainSource, strings.ReplaceAll(originalMain, anchor, "koala.Close(cfg.Server.ShutdownTimeout, telemetry.Shutdown, "+strings.TrimSpace(cleanupRegistration)+")"))
	s.Write(healthImplementation, strings.ReplaceAll(healthSources[healthPaths[1]], "return &Service{isReady: d.IsReady}, nil", "return nil, nil"))
	failure := fail("start")
	s.Contains(failure, "factory returned nil implementation")
	s.NotContains(failure, "Server started")
	marker := p("cleanup-marker")
	s.Compare(s.Read(marker), "closed\n", "cleanup on failure differs")
	s.Remove(marker)
	s.Write(healthImplementation, healthSources[healthPaths[1]])
	serve(func(base, grpc string) { request(base, "/healthz") })
	s.Compare(s.Read(marker), "closed\n", "cleanup on shutdown differs")
	s.Remove(marker)
	s.Write(mainSource, originalMain)
	// Probe paths are application-owned protobuf bindings, requiring no startup edits.
	healthContract := p(healthPaths[0])
	renamedContract := strings.ReplaceAll(healthSources[healthPaths[0]], "/healthz", "/alive")
	renamedContract = strings.ReplaceAll(renamedContract, "/readyz", "/available")
	s.Write(healthContract, renamedContract)
	gen(false, false)
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/available")), map[string]any{"status": "ok"}, "readiness failed")
		s.Compare(jsonObject(request(base, "/alive")), map[string]any{"status": "ok"}, "health failed")
		request(base, "/healthz", 404)
		request(base, "/readyz", 404)
		request(base, "/livez", 404)
	})
	s.Must(os.RemoveAll(p("proto")))
	s.Must(os.RemoveAll(p("internal/service")))
	run("gen")
	s.Check(!s.Exists(embedding) && !s.Exists(p("gen/openapi/api.swagger.json")), "removed service retained spec")
	serve(func(base, grpc string) {
		for _, path := range []string{"/healthz", "/readyz", "/livez"} {
			request(base, path, 404)
		}
	})
	for name, content := range healthSources {
		s.Write(p(name), content)
	}
	gen(false, false)
	s.Compare(digest(project), healthOnly, "restored health generation differs")
	s.Contains(fail("new", "service", "UserService"), "unexpected project arguments")
	s.Check(!s.Exists(p("service")), "removed service command created a project")
	userProto := p("proto/user/v1/user.proto")
	s.Write(userProto, userContract)
	s.Contains(fail("start"), "koala gen")
	shell := p("internal/service/user/service.go")
	s.Check(!s.Exists(shell), "proto creation unexpectedly created an implementation")
	orderProto := p("proto/order/v1/order.proto")
	s.Write(orderProto, strings.ReplaceAll(strings.ReplaceAll(s.Read(userProto), "UserService", "OrderService"), "user", "order"))
	nested := p("cmd/server")
	output := s.Run(nested, s.Env(map[string]string{"KOALA_DATABASE__PASSWORD": "never-export-this-secret"}, ""), false, cli, "config", "dev", "env")
	s.NotContains(output, "never-export-this-secret")
	s.Contains(output, "KOALA_ENV=dev")
	converted := jsonObject(s.Run(nested, nil, false, cli, "config", "dev"))
	s.Compare(converted, jsonObject(s.Read(p("config.dev.json"))), "selected file JSON differs")
	s.Write(p("config.prd.json"), `{"app":{"environment":"production"},"server":{"http":{"port":8123}}}`)
	converted = jsonObject(run("config", "prd"))
	s.Compare(converted, map[string]any{"app": map[string]any{"environment": "production"}, "server": map[string]any{"http": map[string]any{"port": float64(8123)}}}, "production file JSON differs")
	s.Contains(run("config", "prd", "env"), "KOALA_ENV=prd")
	s.Write(filepath.Join(temporary, "config.env"), output)
	s.Run(nested, nil, false, append([]string{cli, "gen"}, generationArgs...)...)
	checkHealthSources()
	s.Contains(s.Read(shell), "UnimplementedUserServiceServer")
	originalShell := s.Read(shell)
	before := digest(project)
	gen(false, false)
	s.Compare(digest(project), before, "repeat generation differs")
	s.Compare(s.Read(shell), originalShell, "handwritten shell overwritten")
	gen(true, true)
	configuration = jsonObject(s.Read(p("config.dev.json")))
	setPort("http", 0)
	setPort("grpc", 0)
	configuration["telemetry"].(map[string]any)["traces"].(map[string]any)["enabled"] = false
	saveConfig()
	serve(func(base, grpc string) {
		request(base, "/healthz")
		request(base, "/v1/user/first", 501)
		request(base, "/v1/order/first", 501)
		spec := paths(request(base, "/openapi.json"))
		s.Check(len(spec) == 4, "unexpected spec paths: %v", spec)
		for _, path := range []string{"/healthz", "/readyz", "/v1/user/{id}", "/v1/order/{id}"} {
			_, ok := spec[path]
			s.Check(ok, "missing spec path: %s", path)
		}
		s.NotContains(request(base, "/docs"), "https://")
	})
	source := strings.ReplaceAll(s.Read(shell), "import (", "import (\n\"context\"") + "\nfunc(s *Service)Get(ctx context.Context,r *pb.GetRequest)(*pb.GetResponse,error){return &pb.GetResponse{Id:r.Id},nil}\n"
	s.Write(shell, source)
	s.Run(project, nil, false, "gofmt", "-w", shell)
	s.Compare(digest(project), before, "implementation-only change affected outputs")
	probe := p("cmd/probe/main.go")
	s.Write(probe, userProbe)
	s.Run(project, nil, false, "go", "build", "-o", filepath.Join(temporary, "probe"), "./cmd/probe")
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/v1/user/rebuilt"))["id"], "rebuilt", "HTTP implementation rebuild failed")
		s.Compare(s.Run(project, nil, false, filepath.Join(temporary, "probe"), grpc), "grpc-proof", "gRPC implementation rebuild failed")
	})
	s.Write(userProto, s.Read(userProto)+"\n// Changed contract input.\n")
	s.Contains(fail("start"), "koala gen")
	gen(false, false)
	s.Check(s.Read(shell) == source || strings.Contains(s.Read(shell), "func (s *Service) Get"), "business method was overwritten")
	savedRegistry := s.Read(registry)
	savedShell := s.Read(shell)
	s.Write(shell, strings.ReplaceAll(savedShell, "//koala:service user.v1.UserService", "//koala:service missing.v1.UnknownService"))
	gen(false, true)
	s.Compare(s.Read(registry), savedRegistry, "failed generation modified registry")
	s.Write(shell, savedShell)
	s.Remove(orderProto)
	s.Must(os.RemoveAll(p("internal/service/order")))
	gen(false, false)
	s.Check(!s.Exists(p("gen/go/order/v1/order.pb.go")), "removed service retained output")
	s.NotContains(s.Read(registry), "OrderService")
	gen(true, false)
	definition := strings.Replace(s.Read(userProto), "}\n\n// GetRequest", "  // Extra is a newly added endpoint.\n  rpc Extra(ExtraRequest) returns (ExtraResponse) {\n    option (google.api.http) = {get: \"/v1/user-extra/{id}\"};\n  }\n}\n\n// GetRequest", 1)
	definition += "\n// ExtraRequest identifies the additional resource.\nmessage ExtraRequest {string id = 1;}\n// ExtraResponse contains its identifier.\nmessage ExtraResponse {string id = 1;}\n"
	s.Write(userProto, definition)
	gen(false, false)
	serve(func(base, grpc string) {
		request(base, "/v1/user-extra/new", 501)
		s.Compare(jsonObject(request(base, "/v1/user/still-implemented"))["id"], "still-implemented", "existing method stopped working")
	})
	gen(true, true)
	s.Write(shell, s.Read(shell)+"\nfunc(s *Service)Extra(ctx context.Context,r *pb.ExtraRequest)(*pb.ExtraResponse,error){return &pb.ExtraResponse{Id:r.Id},nil}\n")
	s.Run(project, nil, false, "gofmt", "-w", shell)
	serve(func(base, grpc string) {
		s.Compare(jsonObject(request(base, "/v1/user-extra/implemented"))["id"], "implemented", "new method implementation failed")
	})
	gen(true, false)
	s.Run(project, nil, false, "go", "build", "-mod=readonly", "-o", filepath.Join(temporary, "server"), "./cmd/server")
	s.Run(root, s.Env(map[string]string{"GOBIN": filepath.Join(temporary, "installed")}, ""), false, "go", "install", "-mod=readonly", "./cmd/koala")
	s.Check(s.Exists(filepath.Join(temporary, "installed/koala")), "CLI was not installed")
	fmt.Println("CLI acceptance passed: manual proto, one-time shells, two services, both transports, inherited fallback, rebuild without generation, freshness, strict checks, ownership, docs, signal forwarding and installation.")
}

const userContract = `syntax = "proto3";

package user.v1;

import "buf/validate/validate.proto";
import "google/api/annotations.proto";

option go_package = "example.com/consumer/gen/go/user/v1;userv1";

// UserService is an application-owned API contract.
service UserService {
  // Get retrieves a resource by ID. Replace this baseline operation as needed.
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = {get: "/v1/user/{id}"};
  }
}

// GetRequest identifies a resource.
message GetRequest {
  string id = 1 [(buf.validate.field).string.min_len = 1];
}

// GetResponse contains the resource identifier.
message GetResponse {
  string id = 1;
}
`

const userProbe = `package main
import("context";"os";"fmt";"time";pb "example.com/consumer/gen/go/user/v1";"google.golang.org/grpc";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();r,e:=pb.NewUserServiceClient(c).Get(ctx,&pb.GetRequest{Id:"grpc-proof"});if e!=nil{panic(e)};fmt.Print(r.Id)}
`

const healthProbe = `package main
import("context";"os";"fmt";"time";pb "example.com/consumer/gen/go/health/v1";"google.golang.org/grpc";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();r,e:=pb.NewHealthServiceClient(c).Health(ctx,&pb.HealthRequest{});if e!=nil{panic(e)};ready,e:=pb.NewHealthServiceClient(c).Ready(ctx,&pb.ReadyRequest{});if e!=nil{panic(e)};if ready.Status!="ok"{panic(ready.Status)};fmt.Print(r.Status)}
`

const readyProbe = `package main
import("context";"os";"time";pb "example.com/consumer/gen/go/health/v1";"google.golang.org/grpc";"google.golang.org/grpc/codes";"google.golang.org/grpc/status";"google.golang.org/grpc/credentials/insecure")
func main(){c,e:=grpc.NewClient(os.Args[1],grpc.WithTransportCredentials(insecure.NewCredentials()));if e!=nil{panic(e)};defer c.Close();ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel();_,e=pb.NewHealthServiceClient(c).Ready(ctx,&pb.ReadyRequest{});if status.Code(e)!=codes.Unavailable{panic(e)}}
`

const cleanupRegistration = `
func(context.Context) error {
 f, err := os.OpenFile("cleanup-marker", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
 if err != nil { return err }
 _, err = f.WriteString("closed\n")
 return errors.Join(err, f.Close())
}
`
