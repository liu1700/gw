package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/liu1700/gw/internal/branchinfo"
	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/registry"
)

// cmdDoctor verifies the real end-to-end path for the current worktree —
// CA trust, proxy, DNS, and each service actually responding through its
// public HTTPS URL — and only reports success when they pass. It exits
// non-zero on any failure so scripts and agents can trust the result.
func cmdDoctor() error {
	d := &doctor{}

	caCert := d.checkCA()
	proxyPort := d.checkProxy()
	d.checkBranch(caCert, proxyPort)

	if d.failed {
		return errors.New("doctor found problems (see above)")
	}
	fmt.Println("all good ✓")
	return nil
}

type doctor struct{ failed bool }

func (d *doctor) ok(msg string)   { fmt.Println("✓ " + msg) }
func (d *doctor) note(msg string) { fmt.Println("~ " + msg) }
func (d *doctor) fail(msg, fix string) {
	d.failed = true
	fmt.Println("✗ " + msg)
	if fix != "" {
		fmt.Println("  → " + fix)
	}
}

const firefoxNote = "  (Firefox keeps its own trust store — if its padlock is red, import the CA there too)"

// checkCA confirms the local CA exists and is parseable; returns it for the
// later trust check, or nil if it's missing/corrupt.
func (d *doctor) checkCA() *x509.Certificate {
	b, err := os.ReadFile(certs.CACertPath())
	if err != nil {
		d.fail("local CA missing", "run `gw trust`")
		return nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		d.fail("local CA file is corrupt", "run `gw trust` to regenerate it")
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		d.fail("local CA file is unreadable: "+err.Error(), "run `gw trust` to regenerate it")
		return nil
	}
	d.ok("local CA exists")
	return c
}

// checkProxy reports whether the gateway is listening and on which port
// (0 if not running).
func (d *doctor) checkProxy() int {
	switch {
	case portOpen(443):
		d.ok("proxy listening on :443")
		return 443
	case portOpen(fallbackProxyPort):
		d.ok(fmt.Sprintf("proxy on fallback :%d (grant :443 with setcap/sudo for portless URLs)", fallbackProxyPort))
		return fallbackProxyPort
	default:
		d.fail("proxy not running", "run `gw proxy -d` (or just `gw up -d`)")
		return 0
	}
}

// checkBranch resolves the current worktree and verifies DNS plus each
// service's real reachability, then settles the CA-trust verdict.
func (d *doctor) checkBranch(caCert *x509.Certificate, proxyPort int) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	path, err := config.Find(cwd)
	if err != nil {
		d.note("not inside a gw repo (no gw.toml) — skipped per-service checks")
		d.checkTrust(caCert, false, false)
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		d.fail("gw.toml failed to load: "+err.Error(), "")
		return
	}
	root := cwd
	if r, err := branchinfo.WorktreeRoot(cwd); err == nil {
		root = r
	}
	info, err := branchinfo.Detect(root)
	if err != nil {
		d.fail("could not determine the branch: "+err.Error(), "")
		return
	}

	// DNS: resolve an actual routed hostname rather than hedging.
	if host := firstRoutedHost(cfg, info); host != "" {
		if addrs, err := net.LookupHost(host); err == nil && has127(addrs) {
			d.ok(host + " resolves to 127.0.0.1")
		} else if strings.HasSuffix(cfg.Domain, ".localhost") {
			d.note(host + ": browsers resolve .localhost natively; CLI tools (curl) may need an /etc/hosts entry")
		} else {
			d.fail(host+" does not resolve to 127.0.0.1",
				"add a wildcard A record *."+cfg.Domain+" → 127.0.0.1 (or run dnsmasq; see README → DNS)")
		}
	}

	// Per-service health. A registered route means the process is alive;
	// for plain HTTP services we additionally make a real request through the
	// proxy, which simultaneously proves the CA is trusted.
	routes, _ := registry.Load()
	trustProven, trustBroken := false, false
	for _, svc := range cfg.Services {
		host := cfg.HostFor(svc.Name, info.Slug, info.IsMain)
		r, registered := routes[host]
		if !registered {
			d.fail(svc.Name+" is not running (no route registered)",
				"run `gw up -d`; if it started then died, check `gw logs`")
			continue
		}
		switch svc.Proxy {
		case config.ProxyNone, config.ProxyPassthrough:
			// No HTTP endpoint to GET — verify the process is holding its port.
			if portOpen(r.Port) {
				d.ok(fmt.Sprintf("%s listening on 127.0.0.1:%d (%s)", svc.Name, r.Port, config.ModeLabel(svc.Proxy)))
			} else {
				d.fail(fmt.Sprintf("%s registered but nothing is listening on 127.0.0.1:%d", svc.Name, r.Port),
					"check `gw logs`")
			}
		default: // plain HTTP behind the proxy
			if proxyPort == 0 {
				continue // proxy already reported down; can't probe
			}
			status, res := probeHTTPS(host, proxyPort)
			switch res {
			case probeOK:
				if status == 502 || status == 503 || status == 504 {
					d.fail(fmt.Sprintf("%s: proxy reached but upstream returned HTTP %d", svc.Name, status),
						"the service is down or still booting — check `gw logs`")
				} else {
					d.ok(fmt.Sprintf("%s responds through https://%s (HTTP %d)", svc.Name, host, status))
					trustProven = true
				}
			case probeCertUntrusted:
				d.fail("TLS to "+host+" is not trusted — the CA is not in the trust store", "run `gw trust`")
				trustBroken = true
			case probeNoResponse:
				d.fail(svc.Name+" did not respond through the proxy",
					"check `gw logs`; ensure the service binds $PORT on 127.0.0.1")
			}
		}
	}

	d.checkTrust(caCert, trustProven, trustBroken)
}

// checkTrust prefers the verdict from a live TLS handshake; absent one it
// falls back to a static system-trust-store check.
func (d *doctor) checkTrust(caCert *x509.Certificate, proven, broken bool) {
	if caCert == nil {
		return // already reported missing
	}
	switch {
	case broken:
		fmt.Println(firefoxNote) // the specific failure was already printed
	case proven:
		d.ok("local CA is trusted (verified by a real TLS handshake)")
		fmt.Println(firefoxNote)
	case caInSystemPool(caCert):
		d.ok("local CA is present in the system trust store")
		fmt.Println(firefoxNote)
	default:
		d.fail("local CA is not trusted by the system store", "run `gw trust`")
		fmt.Println(firefoxNote)
	}
}

func firstRoutedHost(cfg *config.Config, info branchinfo.Info) string {
	for _, s := range cfg.Services {
		if s.Proxy != config.ProxyNone {
			return cfg.HostFor(s.Name, info.Slug, info.IsMain)
		}
	}
	return ""
}

type probeResult int

const (
	probeOK probeResult = iota
	probeCertUntrusted
	probeNoResponse
)

// probeHTTPS makes a real request to a service through the proxy. It dials
// loopback directly and carries the hostname via SNI + Host header, so the
// probe works even when *.localhost is not in DNS (reported separately), while
// still verifying the served certificate against the system trust store.
func probeHTTPS(host string, proxyPort int) (int, probeResult) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
		},
		TLSClientConfig:   &tls.Config{ServerName: host}, // nil RootCAs → system trust
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	resp, err := client.Do(req)
	if err != nil {
		if isCertError(err) {
			return 0, probeCertUntrusted
		}
		return 0, probeNoResponse
	}
	resp.Body.Close()
	return resp.StatusCode, probeOK
}

// isCertError reports whether err is a TLS trust/verification failure (as
// opposed to a connection or protocol error).
func isCertError(err error) bool {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var hn x509.HostnameError
	if errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &hn) {
		return true
	}
	return strings.Contains(err.Error(), "certificate")
}

// caInSystemPool reports whether the self-signed CA verifies against the
// system root pool — true only if it has actually been installed there.
func caInSystemPool(ca *x509.Certificate) bool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return false
	}
	_, err = ca.Verify(x509.VerifyOptions{Roots: pool})
	return err == nil
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func has127(addrs []string) bool {
	for _, a := range addrs {
		if a == "127.0.0.1" || a == "::1" {
			return true
		}
	}
	return false
}
