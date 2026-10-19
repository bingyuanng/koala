package koala_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	koala "github.com/bingyuanng/koala"
)

func TestHTTPDeadlineInterruptsStalledBody(t *testing.T) {
	c := testConfig()
	c.Requests.DefaultTimeout = "100ms"
	a, _, stop := start(t, c, koala.Options{})
	defer stop()
	_, address := a.Addresses()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Send valid headers and stall mid-body without closing the connection.
	if _, err := fmt.Fprint(conn, "POST /v1/users HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled request did not receive a timeout response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(body), `"code":4`) {
		t.Fatalf("got HTTP %d: %s", response.StatusCode, body)
	}
}
