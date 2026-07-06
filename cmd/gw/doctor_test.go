package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func TestHas127(t *testing.T) {
	cases := []struct {
		in   []string
		want bool
	}{
		{[]string{"127.0.0.1"}, true},
		{[]string{"::1"}, true},
		{[]string{"10.0.0.5", "127.0.0.1"}, true},
		{[]string{"10.0.0.5"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := has127(c.in); got != c.want {
			t.Errorf("has127(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsCertError(t *testing.T) {
	trust := []error{
		x509.UnknownAuthorityError{},
		x509.CertificateInvalidError{Reason: x509.Expired},
		x509.HostnameError{},
		fmt.Errorf("tls: failed to verify certificate: x509: certificate signed by unknown authority"),
	}
	for _, err := range trust {
		if !isCertError(err) {
			t.Errorf("isCertError(%v) = false, want true", err)
		}
	}
	other := []error{
		errors.New("connection refused"),
		errors.New("context deadline exceeded"),
	}
	for _, err := range other {
		if isCertError(err) {
			t.Errorf("isCertError(%v) = true, want false", err)
		}
	}
}

// A freshly generated, never-installed CA must not be reported as trusted.
func TestCAInSystemPoolRejectsUnknownCA(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gw test CA (not installed)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if caInSystemPool(ca) {
		t.Error("a never-installed CA was reported as present in the system trust store")
	}
}
