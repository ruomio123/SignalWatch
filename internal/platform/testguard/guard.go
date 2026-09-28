// Package testguard is used only by test entry points, never by application code.
package testguard

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

type loopbackTransport struct{ next http.RoundTripper }

func (t loopbackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("test outbound HTTP denied: %s", host)
	}
	return t.next.RoundTrip(r)
}
func Install() {
	http.DefaultTransport = loopbackTransport{http.DefaultTransport}
	if os.Getenv("SIGNALWATCH_INTEGRATION_REQUIRED") == "1" && os.Getenv("M1_TEST_MYSQL_DSN") == "" {
		fmt.Fprintln(os.Stderr, "integration acceptance requires M1_TEST_MYSQL_DSN")
		os.Exit(1)
	}
}
