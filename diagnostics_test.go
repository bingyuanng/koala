package koala_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	koala "github.com/bingyuanng/koala"
	userv1 "github.com/bingyuanng/koala/internal/testdata/runtime/gen/go/user/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRequestFailureDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		hook koala.Hook
		want string
	}{
		{"error cause", func(ctx context.Context, _ string) (context.Context, error) {
			return ctx, errors.New("dependency operation failed")
		}, `"error":"dependency operation failed"`},
		{"panic stack", func(context.Context, string) (context.Context, error) {
			panic("private secret")
		}, `"stack":`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs lockedBuffer
			_, conn, stop := start(t, testConfig(), koala.Options{
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Authenticate: tt.hook,
			})
			t.Cleanup(stop)
			_, err := userv1.NewUserServiceClient(conn).GetUser(context.Background(), &userv1.GetUserRequest{Id: "1"})
			if status.Code(err) != codes.Internal || status.Convert(err).Message() != "internal server error" {
				t.Fatalf("internal details exposed to client: %v", err)
			}
			logs.Lock()
			output := logs.Buffer.String()
			logs.Unlock()
			if !strings.Contains(output, tt.want) || strings.Contains(output, "private secret") {
				t.Fatalf("missing diagnostics or exposed panic value: %s", output)
			}
		})
	}
}
