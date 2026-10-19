package koala

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func (a *App) transportCredentials() (grpc.ServerOption, credentials.TransportCredentials, error) {
	var dialCredentials credentials.TransportCredentials = insecure.NewCredentials()
	var serverOption grpc.ServerOption
	if a.server.GRPC.CertFile != "" {
		cert, e := tls.LoadX509KeyPair(a.server.GRPC.CertFile, a.server.GRPC.KeyFile)
		if e != nil {
			return nil, nil, errors.New("koala: cannot load TLS certificate")
		}
		serverOption = grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if a.server.GRPC.CAFile != "" {
			pem, e := os.ReadFile(a.server.GRPC.CAFile)
			if e != nil || !roots.AppendCertsFromPEM(pem) {
				return nil, nil, errors.New("koala: cannot load gateway CA")
			}
		}
		name := a.server.GRPC.ServerName
		if name == "" {
			name = "localhost"
		}
		dialCredentials = credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12})
	}
	return serverOption, dialCredentials, nil
}

func waitForGateway(ctx context.Context, conn *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(ctx, state) {
			return fmt.Errorf("koala: gateway connection: %w", ctx.Err())
		}
	}
	return nil
}

func (a *App) gatewayServer(ctx context.Context, conn *grpc.ClientConn) (*http.Server, error) {
	gateway := runtime.NewServeMux(runtime.WithErrorHandler(gatewayErrorHandler), runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
		if strings.EqualFold(key, "x-request-id") {
			return "x-request-id", true
		}
		return "", false
	}))
	for _, r := range a.registrations {
		if err := registerGateway(ctx, r, gateway, conn); err != nil {
			return nil, fmt.Errorf("koala: gateway registration: %w", err)
		}
	}
	if err := waitForGateway(ctx, conn); err != nil {
		return nil, err
	}
	handler, err := a.httpHandler(gateway, a.options.Telemetry)
	if err != nil {
		return nil, err
	}
	write, _ := time.ParseDuration(a.server.HTTP.WriteTimeout)
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      write,
	}, nil
}
