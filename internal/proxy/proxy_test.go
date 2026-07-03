package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/registry"
)

// testGateway runs the full Serve path (SNI dispatch + TLS-terminating HTTP)
// on an ephemeral port with isolated state, and returns its address.
func testGateway(t *testing.T, opts ...func(*Server)) string {
	t.Helper()
	t.Setenv("GW_STATE_DIR", t.TempDir())
	ca, err := certs.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := New()
	for _, o := range opts {
		o(s)
	}
	go s.Serve(ln, ca)
	return ln.Addr().String()
}

// gwClient returns an HTTPS client that dials the gateway regardless of URL
// host (so request Host/SNI drive routing) and trusts the gw CA.
func gwClient(t *testing.T, gwAddr string) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(certs.CACertPath())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("bad gw CA pem")
	}
	dial := func(network, addr string) (net.Conn, error) { return net.Dial("tcp", gwAddr) }
	return &http.Client{Transport: &http.Transport{
		Dial:            dial,
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

func TestPeekClientHelloCapturesSNI(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	go func() {
		tls.Client(cli, &tls.Config{ServerName: "data.feat.demo.localhost", InsecureSkipVerify: true}).Handshake()
		cli.Close()
	}()
	sni, prefix := peekClientHello(srv, 2*time.Second)
	if sni != "data.feat.demo.localhost" {
		t.Errorf("sni = %q", sni)
	}
	if len(prefix) == 0 {
		t.Error("no bytes recorded for replay")
	}
}

// The issue-#1 scenario: an mTLS upstream with its own PKI behind
// proxy = "passthrough". The client's certificate must reach the upstream
// intact, and the upstream's own certificate must reach the client —
// i.e. the gateway must not terminate TLS.
func TestPassthroughMTLSEndToEnd(t *testing.T) {
	gwAddr := testGateway(t)
	const host = "data.feat.demo.localhost"

	appCA, appCAKey := mkCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(appCA)
	serverCert := mkLeaf(t, appCA, appCAKey, "orlop-server", []string{host}, x509.ExtKeyUsageServerAuth)
	clientCert := mkLeaf(t, appCA, appCAKey, "agent-42", nil, x509.ExtKeyUsageClientAuth)

	upLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upLn.Close() })
	go func() {
		for {
			c, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc := c.(*tls.Conn)
				if err := tc.Handshake(); err != nil {
					return
				}
				// Echo the identity we derived from the client leaf cert.
				fmt.Fprintf(c, "hello %s", tc.ConnectionState().PeerCertificates[0].Subject.CommonName)
			}(c)
		}
	}()

	registry.Register(registry.Route{
		Host: host, Port: upLn.Addr().(*net.TCPAddr).Port, PID: os.Getpid(),
		Branch: "feat", Service: "data", Mode: config.ProxyPassthrough,
	})

	conn, err := tls.Dial("tcp", gwAddr, &tls.Config{
		ServerName:   host,
		RootCAs:      pool, // trusts ONLY the app CA — termination by gw would fail here
		Certificates: []tls.Certificate{clientCert},
	})
	if err != nil {
		t.Fatalf("mTLS dial through gateway: %v", err)
	}
	defer conn.Close()
	b, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "hello agent-42" {
		t.Errorf("upstream saw %q, want client-cert identity to survive the gateway", got)
	}
}

func TestHTTPModeStillProxies(t *testing.T) {
	gwAddr := testGateway(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok:%s", r.Host)
	}))
	t.Cleanup(up.Close)
	registry.Register(registry.Route{
		Host: "web.demo.localhost", Port: up.Listener.Addr().(*net.TCPAddr).Port,
		PID: os.Getpid(), Branch: "main", Service: "web",
	})

	resp, err := gwClient(t, gwAddr).Get("https://web.demo.localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok:web.demo.localhost" {
		t.Errorf("status %d body %q", resp.StatusCode, b)
	}
}

func TestNoneModeIsNotRouted(t *testing.T) {
	gwAddr := testGateway(t)
	registry.Register(registry.Route{
		Host: "worker.demo.localhost", Port: 12345, PID: os.Getpid(),
		Branch: "main", Service: "worker", Mode: config.ProxyNone,
	})

	resp, err := gwClient(t, gwAddr).Get("https://worker.demo.localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want 421", resp.StatusCode)
	}
	if !strings.Contains(string(b), "127.0.0.1:12345") {
		t.Errorf("body %q should tell the caller where to connect", b)
	}
}

func TestUnknownHostIs502(t *testing.T) {
	gwAddr := testGateway(t)
	resp, err := gwClient(t, gwAddr).Get("https://nope.demo.localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// A client with no SNI still reaches the terminating path (and its error
// pages) instead of hanging or being dropped.
func TestNoSNIFallsThroughToHTTP(t *testing.T) {
	gwAddr := testGateway(t)
	conn, err := tls.Dial("tcp", gwAddr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: nope.demo.localhost\r\nConnection: close\r\n\r\n")
	b, _ := io.ReadAll(conn)
	if !strings.Contains(string(b), "502") {
		t.Errorf("response %q, want a 502 error page", b)
	}
}

// A connection that never sends a ClientHello is closed at the peek deadline
// rather than leaking.
func TestSilentConnIsClosed(t *testing.T) {
	gwAddr := testGateway(t, func(s *Server) { s.helloTimeout = 200 * time.Millisecond })
	c, err := net.Dial("tcp", gwAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read = %v, want EOF (gateway should close silent conns)", err)
	}
}

// --- minimal PKI for the passthrough upstream (the app's own, not gw's) ---

func mkCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "app test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func mkLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, dns []string, eku x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
