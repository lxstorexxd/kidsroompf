package hub

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"

	"kidsroom-agent/internal/certs"
)

// dualServer answers both plain HTTP and HTTPS on one port.
//
// A real agent answers one scheme at a time and rebinds its listener when
// control.tls changes. Serving both on one port is how a test follows the hub
// across that switch: the hub keeps the host and port and only changes the
// scheme, which is exactly the behaviour under test, and nothing here has to
// race a rebind.
type dualServer struct {
	ln   net.Listener
	cert *x509.Certificate

	closeOnce sync.Once
	done      chan struct{}
}

func newDualServer(t *testing.T, h http.Handler) *dualServer {
	t.Helper()

	certPEM, keyPEM, err := certs.GenerateSelfSigned("localhost")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := certs.LoadPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	d := &dualServer{ln: ln, cert: pair.Leaf, done: make(chan struct{})}

	plain := newChanListener(ln.Addr(), d.done)
	secure := newChanListener(ln.Addr(), d.done)

	go func() { _ = http.Serve(plain, h) }()
	go func() {
		_ = http.Serve(tls.NewListener(secure, &tls.Config{Certificates: []tls.Certificate{*pair}}), h)
	}()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.dispatch(c, plain, secure)
		}
	}()

	t.Cleanup(d.Close)
	return d
}

// dispatch sends the connection to the plain or the TLS server depending on its
// first byte: a TLS handshake always starts with 0x16.
func (d *dualServer) dispatch(c net.Conn, plain, secure *chanListener) {
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}

	conn := &peekConn{Conn: c, r: br}
	target := plain
	if first[0] == 0x16 {
		target = secure
	}
	select {
	case target.conns <- conn:
	case <-d.done:
		c.Close()
	}
}

func (d *dualServer) Close() {
	d.closeOnce.Do(func() {
		close(d.done)
		_ = d.ln.Close()
	})
}

// URL is the address on one scheme.
func (d *dualServer) URL(scheme string) string {
	return scheme + "://" + d.ln.Addr().String()
}

// peekConn hands back the byte that was read to decide the scheme.
type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// chanListener is a net.Listener fed by the dispatcher instead of by the
// network.
type chanListener struct {
	conns chan net.Conn
	addr  net.Addr
	done  chan struct{}
}

func newChanListener(addr net.Addr, done chan struct{}) *chanListener {
	return &chanListener{conns: make(chan net.Conn), addr: addr, done: done}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, errors.New("стенд остановлен")
	}
}

func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }
