package certs

import (
	"crypto/x509"
	"os"
	"strings"
	"testing"
)

// CombinedBundlePath must produce a PEM that carries both the local gw CA and
// the public system roots, so a service pointed at it via REQUESTS_CA_BUNDLE /
// SSL_CERT_FILE trusts branch URLs *and* its own outbound HTTPS to public CAs.
func TestCombinedBundleHasSystemRootsAndGWCA(t *testing.T) {
	t.Setenv("GW_STATE_DIR", t.TempDir())
	if _, err := LoadOrCreate(); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Skip cleanly on platforms where we can't locate public roots (the code
	// falls back to the CA-only path there — nothing to combine).
	if len(systemRootsPEM()) == 0 {
		t.Skip("no system root bundle on this platform")
	}

	path := CombinedBundlePath()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	// The gw CA cert must be present verbatim.
	caPEM, _ := os.ReadFile(CACertPath())
	if !strings.Contains(string(b), strings.TrimSpace(string(caPEM))) {
		t.Error("combined bundle does not contain the gw CA cert")
	}

	// It must parse and contain many roots (i.e. not just the single gw CA).
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		t.Fatal("combined bundle contains no parseable certificates")
	}
	if n := strings.Count(string(b), "BEGIN CERTIFICATE"); n < 2 {
		t.Errorf("expected system roots + gw CA, got only %d cert(s)", n)
	}
}

// A second call should reuse the cached file rather than regenerate it.
func TestCombinedBundleCaches(t *testing.T) {
	t.Setenv("GW_STATE_DIR", t.TempDir())
	if _, err := LoadOrCreate(); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if len(systemRootsPEM()) == 0 {
		t.Skip("no system root bundle on this platform")
	}

	first := CombinedBundlePath()
	fi1, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	second := CombinedBundlePath()
	if first != second {
		t.Fatalf("path changed between calls: %q vs %q", first, second)
	}
	fi2, _ := os.Stat(second)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Error("bundle was regenerated on the second call")
	}
}
