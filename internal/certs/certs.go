// Package certs implements a local CA and on-demand leaf certificate signing.
//
// Design: instead of pre-signing a wildcard cert, the proxy's tls.Config uses
// GetCertificate to mint a leaf for whatever SNI name arrives, signed by a
// local CA that `gw trust` has installed into the system trust store. Any
// branch subdomain therefore gets a valid cert with zero configuration.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	mu    sync.Mutex
	cache map[string]*tls.Certificate // SNI -> leaf
}

func stateDir() string {
	if d := os.Getenv("GW_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gw")
}

func caPaths() (certPath, keyPath string) {
	return filepath.Join(stateDir(), "ca.pem"), filepath.Join(stateDir(), "ca.key")
}

// LoadOrCreate returns the local CA, creating and persisting it on first run.
func LoadOrCreate() (*CA, error) {
	certPath, keyPath := caPaths()
	if c, err := load(certPath, keyPath); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"gw local CA"}, CommonName: "gw local CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	if err := writePEM(keyPath, "EC PRIVATE KEY", kder, 0o600); err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return &CA{cert: cert, key: key, cache: map[string]*tls.Certificate{}}, nil
}

func load(certPath, keyPath string) (*CA, error) {
	cp, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	kp, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	cb, _ := pem.Decode(cp)
	kb, _ := pem.Decode(kp)
	if cb == nil || kb == nil {
		return nil, fmt.Errorf("corrupt CA files")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, cache: map[string]*tls.Certificate{}}, nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}

// GetCertificate is plugged into tls.Config; it signs (and caches) a leaf
// certificate for the requested server name on the fly.
func (c *CA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := hello.ServerName
	if name == "" {
		name = "localhost"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if leaf, ok := c.cache[name]; ok {
		return leaf, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
	}
	c.cache[name] = leaf
	return leaf, nil
}

// CACertPath returns the PEM path, for NODE_EXTRA_CA_CERTS / REQUESTS_CA_BUNDLE injection.
func CACertPath() string { p, _ := caPaths(); return p }

// Trust installs the CA into the trust store. Best-effort per platform;
// prints manual instructions on failure. (Swap for smallstep/truststore later.)
func Trust() error {
	if _, err := LoadOrCreate(); err != nil {
		return err
	}
	certPath, _ := caPaths()
	switch runtime.GOOS {
	case "darwin":
		// User trust domain is enough for browsers and needs no sudo.
		home, _ := os.UserHomeDir()
		login := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
		if err := run("security", "add-trusted-cert", "-r", "trustRoot", "-k", login, certPath); err == nil {
			fmt.Println("gw: CA trusted for your user (login keychain, no sudo needed)")
			return nil
		}
		fmt.Println("gw: login keychain failed, trying system keychain (needs sudo)")
		return run("sudo", "security", "add-trusted-cert", "-d",
			"-k", "/Library/Keychains/System.keychain", certPath)
	case "linux":
		dst := "/usr/local/share/ca-certificates/gw-local-ca.crt"
		if err := run("sudo", "cp", certPath, dst); err != nil {
			return err
		}
		return run("sudo", "update-ca-certificates")
	default:
		return fmt.Errorf("please add %s to your system trust store manually", certPath)
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}
