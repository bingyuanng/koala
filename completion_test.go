package koala_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	koala "github.com/bingyuanng/koala"
	userv1 "github.com/bingyuanng/koala/internal/testdata/runtime/gen/go/user/v1"
	"google.golang.org/grpc/metadata"
)

func TestOneCompletionLogPerRequest(t *testing.T) {
	for _, tt := range []struct {
		name, transport, method, path, body string
		status                              int
	}{
		{"HTTP success", "http", "GET", "/v1/users/1", "", 200},
		{"HTTP decoding failure", "http", "POST", "/v1/users", `{`, 400},
		{"HTTP RPC failure", "http", "GET", "/v1/users/panic", "", 500},
		{name: "direct gRPC with spoofed marker", transport: "grpc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs lockedBuffer
			// Cleanups run in reverse order: drain the app before checking its logs.
			t.Cleanup(func() {
				logs.Lock()
				output := logs.Buffer.String()
				logs.Unlock()
				count := 0
				decoder := json.NewDecoder(strings.NewReader(output))
				for {
					var record map[string]any
					if err := decoder.Decode(&record); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if record["msg"] == "HTTP completed" || record["msg"] == "RPC completed" {
						count++
						if record["transport"] != tt.transport {
							t.Errorf("completion transport = %v, want %s", record["transport"], tt.transport)
						}
					}
				}
				if count != 1 {
					t.Errorf("got %d completion records, want 1\n%s", count, output)
				}
			})
			hook := func(ctx context.Context, _ string) (context.Context, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if len(md.Get("koala-gateway-completion")) != 0 {
					t.Error("internal logging marker exposed to application hook")
				}
				return ctx, nil
			}
			a, conn, stop := start(t, testConfig(), koala.Options{
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Authenticate: hook,
			})
			t.Cleanup(stop)
			if tt.transport == "grpc" {
				ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("koala-gateway-completion", "spoofed"))
				if _, err := userv1.NewUserServiceClient(conn).GetUser(ctx, &userv1.GetUserRequest{Id: "1"}); err != nil {
					t.Fatal(err)
				}
				return
			}
			_, addr := a.Addresses()
			req, err := http.NewRequest(tt.method, "http://"+addr+tt.path, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Koala-Gateway-Completion", "spoofed")
			req.Header.Set("Grpc-Metadata-Koala-Gateway-Completion", "spoofed")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Fatalf("got HTTP %d, want %d", resp.StatusCode, tt.status)
			}
		})
	}
}
