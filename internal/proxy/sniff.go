// ClientHello peeking for SNI routing, dependency-free (the tcpproxy trick):
// run a throwaway tls.Server handshake over a read-only view of the
// connection, capture the SNI from GetConfigForClient, and record the bytes
// consumed so they can be replayed to whoever really handles the connection.
package proxy

import (
	"bytes"
	"errors"
	"io"
	"net"
	"time"

	"crypto/tls"
)

// peekClientHello returns the connection's SNI ("" if absent or not TLS) and
// the bytes consumed while looking. The caller must replay those bytes —
// via newPrefixConn or by writing them to the upstream — before any further
// reads from c.
func peekClientHello(c net.Conn, timeout time.Duration) (sni string, prefix []byte) {
	c.SetReadDeadline(time.Now().Add(timeout))
	defer c.SetReadDeadline(time.Time{})

	var buf bytes.Buffer
	// The handshake parses the ClientHello and fires GetConfigForClient,
	// then fails ("no certificates") before ever writing — leaving exactly
	// the ClientHello bytes in buf. Its error is expected and discarded.
	_ = tls.Server(readOnlyConn{io.TeeReader(c, &buf)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = h.ServerName
			return nil, nil
		},
	}).Handshake()
	return sni, buf.Bytes()
}

var errReadOnly = errors.New("gw: sniff conn is read-only")

// readOnlyConn feeds the peeking handshake; writes fail so it can never
// leak handshake bytes onto the real connection.
type readOnlyConn struct{ r io.Reader }

func (c readOnlyConn) Read(p []byte) (int, error)         { return c.r.Read(p) }
func (c readOnlyConn) Write(p []byte) (int, error)        { return 0, errReadOnly }
func (c readOnlyConn) Close() error                       { return nil }
func (c readOnlyConn) LocalAddr() net.Addr                { return nil }
func (c readOnlyConn) RemoteAddr() net.Addr               { return nil }
func (c readOnlyConn) SetDeadline(t time.Time) error      { return nil }
func (c readOnlyConn) SetReadDeadline(t time.Time) error  { return nil }
func (c readOnlyConn) SetWriteDeadline(t time.Time) error { return nil }

// prefixConn replays peeked bytes, then continues reading from the real conn.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func newPrefixConn(c net.Conn, prefix []byte) net.Conn {
	return prefixConn{Conn: c, r: io.MultiReader(bytes.NewReader(prefix), c)}
}

func (c prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener adapts the accept loop to http.Server.ServeTLS: connections
// that need TLS termination are pushed into ch.
type chanListener struct {
	ch   chan net.Conn
	addr net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *chanListener) Close() error   { return nil } // lifetime == process
func (l *chanListener) Addr() net.Addr { return l.addr }
