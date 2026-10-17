package koala_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	koala "github.com/bingyuanng/koala"
	userv1 "github.com/bingyuanng/koala/examples/users/gen/go/user/v1"
	"github.com/bingyuanng/koala/observability"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestTLSGateway(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	c := testConfig()
	c.Server.GRPC.CertFile = certFile
	c.Server.GRPC.KeyFile = keyFile
	c.Server.GRPC.CAFile = certFile
	a, e := koala.New(c, koala.Options{})
	if e != nil {
		t.Fatal(e)
	}
	a.Register(registration())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	select {
	case <-a.Ready():
	case e := <-done:
		t.Fatal(e)
	case <-time.After(10 * time.Second):
		t.Fatal("TLS startup timeout")
	}
	ga, ha := a.Addresses()
	creds, e := credentials.NewClientTLSFromFile(certFile, "localhost")
	if e != nil {
		t.Fatal(e)
	}
	conn, e := grpc.NewClient(ga, grpc.WithTransportCredentials(creds))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}); e != nil {
		t.Fatal(e)
	}
	resp, e := http.Get("http://" + ha + "/v1/users/1")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}
func TestMetricsWithoutTraceExportAndCallerDeadline(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer mp.Shutdown(context.Background())
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	a, conn, stop := start(t, testConfig(), koala.Options{Telemetry: &observability.Providers{Traces: tp, Metrics: mp}})
	_ = a
	defer stop()
	client := userv1.NewUserServiceClient(conn)
	client.CreateUser(context.Background(), &userv1.CreateUserRequest{})
	client.GetUser(context.Background(), &userv1.GetUserRequest{Id: "panic"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	before := time.Now()
	_, e := client.GetUser(ctx, &userv1.GetUserRequest{Id: "wait"})
	if status.Code(e) != codes.DeadlineExceeded || time.Since(before) > time.Second {
		t.Fatal("short caller deadline not preserved", e)
	}
	var data metricdata.ResourceMetrics
	if e = reader.Collect(context.Background(), &data); e != nil {
		t.Fatal(e)
	}
	counts := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, point := range sum.DataPoints {
					counts[m.Name] += point.Value
				}
			}
		}
	}
	if counts["koala.rpc.validation_failures"] != 1 || counts["koala.rpc.panics"] != 1 {
		t.Fatalf("metrics: %v", counts)
	}
}
func TestUnexpectedListenerFailure(t *testing.T) {
	listeners := []net.Listener{}
	a, e := koala.New(testConfig(), koala.Options{Listen: func(n, addr string) (net.Listener, error) {
		l, e := net.Listen(n, addr)
		listeners = append(listeners, l)
		return l, e
	}})
	if e != nil {
		t.Fatal(e)
	}
	a.Register(registration())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-a.Ready():
	case e := <-done:
		t.Fatal(e)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	listeners[0].Close()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("failure cause lost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("other server did not stop")
	}
	if conn, e := net.DialTimeout("tcp", listeners[1].Addr().String(), 100*time.Millisecond); e == nil {
		conn.Close()
		t.Fatal("HTTP listener leaked")
	}
}
func TestDocsDisablementAndCORS(t *testing.T) {
	c := testConfig()
	c.Server.HTTP.Docs = false
	c.Server.HTTP.AllowedOrigins = []string{"https://allowed.example"}
	a, _, stop := start(t, c, koala.Options{OpenAPI: []byte(`{}`)})
	defer stop()
	_, addr := a.Addresses()
	for _, path := range []string{"/docs", "/openapi.json"} {
		resp, e := http.Get("http://" + addr + path)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("documentation exposed", path)
		}
	}
	req, _ := http.NewRequest("OPTIONS", "http://"+addr+"/v1/users/1", nil)
	req.Header.Set("Origin", "https://allowed.example")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", "http://"+addr+"/v1/users/1", strings.NewReader(""))
	req.Header.Set("Origin", "https://blocked.example")
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
}

func TestUnknownPolicyAndDuplicateRegistration(t *testing.T) {
	c := testConfig()
	c.Requests.MethodTimeouts = map[string]string{"missing.v1.Unknown/Get": "1s"}
	a, e := koala.New(c, koala.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Run(context.Background()); e == nil {
		t.Fatal("unknown method accepted")
	}
	a, e = koala.New(testConfig(), koala.Options{})
	if e != nil {
		t.Fatal(e)
	}
	a.Register(registration())
	if a.Register(registration()) == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestHookCannotDetachDeadlineOrCrashWithNilContext(t *testing.T) {
	hook := func(ctx context.Context, method string) (context.Context, error) { return context.Background(), nil }
	c := testConfig()
	c.Requests.DefaultTimeout = "20ms"
	_, conn, stop := start(t, c, koala.Options{Authenticate: hook})
	client := userv1.NewUserServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := client.GetUser(ctx, &userv1.GetUserRequest{Id: "wait"}); status.Code(e) != codes.DeadlineExceeded {
		t.Fatal(e)
	}
	stop()
	hook = func(ctx context.Context, method string) (context.Context, error) { return nil, nil }
	_, conn, stop = start(t, testConfig(), koala.Options{Authenticate: hook})
	defer stop()
	client = userv1.NewUserServiceClient(conn)
	if _, e := client.GetUser(context.Background(), &userv1.GetUserRequest{Id: "1"}); status.Code(e) != codes.Internal {
		t.Fatal(e)
	}
}
