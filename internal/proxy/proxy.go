// Package proxy is the gateway daemon: one HTTPS listener, Host-header
// routing to per-branch upstream ports, TLS via on-demand leaf signing.
// httputil.ReverseProxy transparently handles WebSocket upgrades.
package proxy

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/registry"
)

const loopHeader = "X-Gw-Proxied"

type Server struct {
	routes *registry.Cached
}

func New() *Server { return &Server{routes: registry.NewCached()} }

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
	tlsCfg := &tls.Config{GetCertificate: ca.GetCertificate, NextProtos: []string{"h2", "http/1.1"}}
	srv := &http.Server{Handler: New(), TLSConfig: tlsCfg}

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
	return srv.ServeTLS(ln, "", "")
}
