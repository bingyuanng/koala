package koala_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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

func TestTLSStartupDiagnostics(t *testing.T) {
	certFile, keyFile := writeTLSCertificate(t)
	_, otherKey := writeTLSCertificate(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "SECRET-missing")
	invalid := filepath.Join(dir, "SECRET-invalid")
	if err := os.WriteFile(invalid, []byte("SECRET invalid certificate material"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*koala.GRPCConfig)
		want   string
	}{
		{"missing certificate", func(c *koala.GRPCConfig) { c.CertFile = missing }, "koala: cannot read TLS certificate: file does not exist"},
		{"missing private key", func(c *koala.GRPCConfig) { c.KeyFile = missing }, "koala: cannot read TLS private key: file does not exist"},
		{"certificate I/O failure", func(c *koala.GRPCConfig) { c.CertFile = dir }, "koala: cannot read TLS certificate: file I/O error"},
		{"invalid certificate", func(c *koala.GRPCConfig) { c.CertFile = invalid }, "koala: invalid TLS certificate or private key pair"},
		{"invalid private key", func(c *koala.GRPCConfig) { c.KeyFile = invalid }, "koala: invalid TLS certificate or private key pair"},
		{"mismatched pair", func(c *koala.GRPCConfig) { c.KeyFile = otherKey }, "koala: invalid TLS certificate or private key pair"},
		{"missing CA", func(c *koala.GRPCConfig) { c.CAFile = missing }, "koala: cannot read gateway CA: file does not exist"},
		{"invalid CA", func(c *koala.GRPCConfig) { c.CAFile = invalid }, "koala: gateway CA contains no valid certificates"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := testConfig()
			config.Server.GRPC.CertFile, config.Server.GRPC.KeyFile = certFile, keyFile
			tt.change(&config.Server.GRPC)
			app, err := newTestApp(t, config, koala.Options{Listen: func(string, string) (net.Listener, error) {
				t.Error("invalid TLS configuration reached listener startup")
				return nil, errors.New("unexpected listener")
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Run(context.Background()); err == nil || err.Error() != tt.want {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}
