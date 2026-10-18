package koala_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	koala "github.com/bingyuanng/koala"
	userv1 "github.com/bingyuanng/koala/internal/testdata/runtime/gen/go/user/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestOneCompletionLogPerRequest(t *testing.T) {
	var logs lockedBuffer
	c := testConfig()
	c.Requests.DefaultTimeout = "30ms"
	hook := func(ctx context.Context, _ string) (context.Context, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if len(md.Get("koala-gateway-completion")) != 0 {
			t.Error("internal logging marker exposed to application hook")
		}
		if len(md.Get("authorization")) > 0 {
			return ctx, status.Error(codes.Unauthenticated, "authentication required")
		}
		return ctx, nil
	}
	a, conn, stop := start(t, c, koala.Options{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Authenticate: hook,
	})
	// Drain HTTP handlers before reading their deferred completion records.
	var stopped sync.Once
	t.Cleanup(func() { stopped.Do(stop) })
	_, addr := a.Addresses()
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/users/1", "", 200},
		{"POST", "/v1/users", `{`, 400},
		{"POST", "/v1/users", `{"name":""}`, 400},
		{"GET", "/v1/users/panic", "", 500},
		{"GET", "/v1/users/wait", "", 504},
		{"GET", "/v1/users/rejected", "", 401},
	} {
		req, err := http.NewRequest(tt.method, "http://"+addr+tt.path, strings.NewReader(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		// Neither raw nor grpc-metadata headers may control logging.
		req.Header.Set("Koala-Gateway-Completion", "spoofed")
		req.Header.Set("Grpc-Metadata-Koala-Gateway-Completion", "spoofed")
		if tt.status == 401 {
			req.Header.Set("Authorization", "reject")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.status {
			t.Fatalf("%s: got %d, want %d", tt.path, resp.StatusCode, tt.status)
		}
	}
	client := userv1.NewUserServiceClient(conn)
	for _, tt := range []struct {
		id   string
		code codes.Code
	}{{"1", codes.OK}, {"", codes.InvalidArgument}, {"panic", codes.Internal}, {"wait", codes.DeadlineExceeded}, {"rejected", codes.Unauthenticated}} {
		md := metadata.Pairs("koala-gateway-completion", "spoofed")
		if tt.code == codes.Unauthenticated {
			md.Set("authorization", "reject")
		}
		ctx := metadata.NewOutgoingContext(context.Background(), md)
		_, err := client.GetUser(ctx, &userv1.GetUserRequest{Id: tt.id})
		if status.Code(err) != tt.code {
			t.Fatalf("%s: got %v, want %v", tt.id, err, tt.code)
		}
	}
	stopped.Do(stop)
	logs.Lock()
	output := logs.Buffer.String()
	logs.Unlock()
	counts := map[string]int{}
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "HTTP completed" || record["msg"] == "RPC completed" {
			counts[record["transport"].(string)]++
			if record["duration_ms"] == nil || record["status"] == nil {
				t.Fatalf("missing completion attributes: %v", record)
			}
		}
	}
	if counts["http"] != 6 || counts["grpc"] != 5 {
		t.Fatalf("expected 6 HTTP and 5 direct gRPC completion records, got %v\n%s", counts, output)
	}
}
