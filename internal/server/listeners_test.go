package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T, network, address string) int {
	t.Helper()
	l, err := net.Listen(network, net.JoinHostPort(address, "0"))
	if err != nil {
		t.Skipf("%s unavailable: %v", network, err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestListenerHTTPAndHTTPSIndependent(t *testing.T) {
	dir := t.TempDir()
	cert, err := EnsureCertificate(filepath.Join(dir, "cert"), filepath.Join(dir, "key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	httpPort := freePort(t, "tcp4", "127.0.0.1")
	httpsPort := freePort(t, "tcp4", "127.0.0.1")
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	g, err := Start(context.Background(), ListenerConfig{
		HTTP:     Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: httpPort},
		HTTPS:    Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: httpsPort},
		CertFile: cert.CertFile, KeyFile: cert.KeyFile,
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Shutdown(context.Background())
	for _, raw := range []string{fmt.Sprintf("http://127.0.0.1:%d", httpPort), fmt.Sprintf("https://127.0.0.1:%d", httpsPort)} {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		res, err := client.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(body) != "ok" {
			t.Fatalf("%s body %q", raw, body)
		}
	}
}

func TestListenerIndependentDisablement(t *testing.T) {
	dir := t.TempDir()
	cert, err := EnsureCertificate(filepath.Join(dir, "cert"), filepath.Join(dir, "key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  ListenerConfig
		url  string
	}{
		{"http only", ListenerConfig{HTTP: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: freePort(t, "tcp4", "127.0.0.1")}}, "http"},
		{"https only", ListenerConfig{HTTPS: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: freePort(t, "tcp4", "127.0.0.1")}, CertFile: cert.CertFile, KeyFile: cert.KeyFile}, "https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := Start(context.Background(), tc.cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
			if err != nil {
				t.Fatal(err)
			}
			defer g.Shutdown(context.Background())
			port := tc.cfg.HTTP.Port
			if tc.url == "https" {
				port = tc.cfg.HTTPS.Port
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			res, err := client.Get(fmt.Sprintf("%s://127.0.0.1:%d", tc.url, port))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
		})
	}
}

func TestListenerIPv6OnlyDoesNotClaimIPv4(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("socket option asserted on Linux")
	}
	port := freePort(t, "tcp6", "::1")
	g, err := Start(context.Background(), ListenerConfig{HTTP: Endpoint{Enabled: true, Addr6: "::", Port: port}}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Skipf("IPv6 wildcard unavailable: %v", err)
	}
	defer g.Shutdown(context.Background())
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		t.Fatalf("IPv6 listener also claimed IPv4: %v", err)
	}
	v4.Close()
}

func TestListenerBindFailureCleansUp(t *testing.T) {
	port := freePort(t, "tcp4", "127.0.0.1")
	occupied, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port+1)))
	if err != nil {
		t.Skipf("cannot reserve second port: %v", err)
	}
	defer occupied.Close()
	dir := t.TempDir()
	cert, certErr := EnsureCertificate(filepath.Join(dir, "cert"), filepath.Join(dir, "key"), nil)
	if certErr != nil {
		t.Fatal(certErr)
	}
	_, err = Start(context.Background(), ListenerConfig{HTTP: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: port}, HTTPS: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: port + 1}, CertFile: cert.CertFile, KeyFile: cert.KeyFile}, http.NewServeMux())
	if err == nil {
		t.Fatal("bind failure accepted")
	}
	reopened, reopenErr := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if reopenErr != nil {
		t.Fatalf("first listener leaked: %v (start: %v)", reopenErr, err)
	}
	reopened.Close()
}

func TestListenerRequiresEnabledAddressAndValidTLS(t *testing.T) {
	tests := []ListenerConfig{
		{},
		{HTTP: Endpoint{Enabled: true, Port: 8377}},
		{HTTPS: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: freePort(t, "tcp4", "127.0.0.1")}, CertFile: "missing", KeyFile: "missing"},
	}
	for _, cfg := range tests {
		if g, err := Start(context.Background(), cfg, http.NewServeMux()); err == nil {
			g.Shutdown(context.Background())
			t.Fatalf("accepted %#v", cfg)
		}
	}
}

func TestListenerGracefulShutdownAndContext(t *testing.T) {
	port := freePort(t, "tcp4", "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	g, err := Start(ctx, ListenerConfig{HTTP: Endpoint{Enabled: true, Addr4: "127.0.0.1", Port: port}}, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Shutdown(context.Background())
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 20*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(strings.TrimSpace("listener remained open"))
}
