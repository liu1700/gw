// Package proxy is the gateway daemon: one listener, SNI/Host routing to
// per-branch upstream ports. Per-service modes (gw.toml `proxy = ...`):
//
//	http         terminate TLS (on-demand leaf signing), reverse-proxy HTTP;
//	             httputil.ReverseProxy transparently handles WebSockets
//	passthrough  splice raw TCP by SNI — TLS is NOT terminated, so upstream
//	             mTLS / client certificates survive end to end
//	none         never routed (gw only supervises the process)
package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/registry"
)

const (
	loopHeader  = "X-Gw-Proxied"
	dialTimeout = 3 * time.Second // upstream connect timeout
)

type Server struct {
	routes       *registry.Cached
	helloTimeout time.Duration // max wait for a ClientHello
}

func New() *Server {
	return &Server{routes: registry.NewCached(), helloTimeout: 10 * time.Second}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	route, ok := s.routes.Lookup(host)
	if ok && !registry.Alive(route) {
		// The process that registered this route died without cleanup.
		registry.Unregister(host)
		ok = false
	}
	if !ok {
		http.Error(w, fmt.Sprintf(
			"gw: no route for %q\nIs the service running? Try `gw up -d` in the worktree, or `gw list`.",
			host), http.StatusBadGateway)
		return
	}
	switch route.Mode {
	case config.ProxyNone:
		http.Error(w, fmt.Sprintf(
			"gw: %q is declared `proxy = \"none\"` — gw supervises it but does not route it.\nConnect directly to 127.0.0.1:%d.",
			host, route.Port), http.StatusMisdirectedRequest)
		return
	case config.ProxyPassthrough:
		// Passthrough is routed at the TLS layer by SNI; an HTTP request for
		// this host means the client connected without (or with a different)
		// server name.
		http.Error(w, fmt.Sprintf(
			"gw: %q is a TLS-passthrough service — speak TLS to it directly with SNI %q (gw does not terminate or proxy HTTP for it).",
			host, host), http.StatusMisdirectedRequest)
		return
	}
	// Loop protection: an app proxying back to its own public hostname
	// without rewriting Host would recurse forever. Detect and fail fast
	// with a hint (same failure mode portless flags as 508).
	if r.Header.Get(loopHeader) != "" {
		http.Error(w, "gw: routing loop detected — your app is proxying to its own gw hostname; point it at a sibling service URL (see GW_URL_* env vars) or rewrite the Host header", http.StatusLoopDetected)
		return
	}

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", route.Port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // preserve public host for the app
			pr.Out.Header.Set(loopHeader, "1")
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
		},
	}
	rp.ServeHTTP(w, r)
}

// Listen starts the HTTPS gateway. Tries :443, falls back to fallbackPort
// when binding is not permitted (no sudo / no setcap).
func Listen(fallbackPort int) error {
	ca, err := certs.LoadOrCreate()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", ":443")
	if err != nil {
		fmt.Printf("gw: cannot bind :443 (%v)\n", err)
		fmt.Printf("gw: falling back to :%d — to use :443 run\n", fallbackPort)
		fmt.Println("      sudo setcap cap_net_bind_service=+ep $(which gw)   # linux")
		fmt.Println("      (macOS 10.14+ allows :443 without root)")
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", fallbackPort))
		if err != nil {
			return err
		}
	}
	fmt.Printf("gw: proxy listening on %s\n", ln.Addr())
	return New().Serve(ln, ca)
}

// Serve runs the gateway on ln. Each connection's ClientHello is peeked for
// SNI: passthrough routes are spliced raw (TLS untouched, client certs reach
// the upstream), everything else goes to the TLS-terminating reverse proxy.
func (s *Server) Serve(ln net.Listener, ca *certs.CA) error {
	tlsCfg := &tls.Config{GetCertificate: ca.GetCertificate, NextProtos: []string{"h2", "http/1.1"}}
	httpLn := &chanListener{ch: make(chan net.Conn), addr: ln.Addr()}
	srv := &http.Server{Handler: s, TLSConfig: tlsCfg}
	go srv.ServeTLS(httpLn, "", "")

	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.dispatch(c, httpLn)
	}
}

// dispatch decides, per connection, between raw TCP splice and TLS termination.
func (s *Server) dispatch(c net.Conn, httpLn *chanListener) {
	sni, prefix := peekClientHello(c, s.helloTimeout)
	if len(prefix) == 0 {
		c.Close() // nothing sent within the deadline — don't hold the fd
		return
	}
	if route, ok := s.routes.Lookup(strings.ToLower(sni)); ok && route.Mode == config.ProxyPassthrough {
		if !registry.Alive(route) {
			registry.Unregister(route.Host)
			c.Close()
			return
		}
		splice(c, prefix, route.Port)
		return
	}
	// Everything else — http routes, unknown hosts, no SNI — is answered by
	// the HTTP server so the user gets a diagnosable error page.
	httpLn.ch <- newPrefixConn(c, prefix)
}

// splice connects the client to 127.0.0.1:port and copies bytes both ways,
// starting with the already-peeked ClientHello.
func splice(client net.Conn, prefix []byte, port int) {
	defer client.Close()
	up, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), dialTimeout)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := up.Write(prefix); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(up, client)
		if t, ok := up.(*net.TCPConn); ok {
			t.CloseWrite() // forward client's EOF
		}
	}()
	io.Copy(client, up)
	if t, ok := client.(*net.TCPConn); ok {
		t.CloseWrite() // forward upstream's EOF
	}
	<-done
}
