package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startSSHServer runs an SSH server on a loopback port for the duration of the
// test.
//
// When answerRequests is false the server completes the handshake and then
// leaves global requests unanswered forever. That is what a keepalive meets
// when a node has moved to a new address: the socket is still open as far as
// this end is concerned, the bytes leave, and no reply and no refusal ever
// comes back.
func startSSHServer(t *testing.T, answerRequests bool) net.Listener {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("build host key signer: %v", err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				serverConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					_ = conn.Close()
					return
				}
				t.Cleanup(func() { _ = serverConn.Close() })
				go func() {
					for ch := range chans {
						_ = ch.Reject(ssh.Prohibited, "not needed by this test")
					}
				}()
				if answerRequests {
					ssh.DiscardRequests(reqs)
				}
				// Otherwise reqs is deliberately never drained.
			}()
		}
	}()
	return ln
}

func dialTestClient(t *testing.T, ln net.Listener) *Client {
	t.Helper()

	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &Client{Client: c}
}

func TestAliveReportsAnsweringConnection(t *testing.T) {
	client := dialTestClient(t, startSSHServer(t, true))

	if !alive(context.Background(), client) {
		t.Fatal("alive() said a responsive connection was dead")
	}
}

func TestAliveReportsClosedConnection(t *testing.T) {
	client := dialTestClient(t, startSSHServer(t, true))
	if err := client.Client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	if alive(context.Background(), client) {
		t.Fatal("alive() said a closed connection was healthy")
	}
}

// TestAliveGivesUpOnSilentConnection is the regression guard. Without a bound
// on the keepalive, a node that vanished held every caller of Pool.Get for as
// long as the kernel kept retransmitting — a quarter of an hour — which showed
// up as a node stuck on "online" and as enrollment reports dying at the proxy.
func TestAliveGivesUpOnSilentConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out aliveTimeout")
	}

	client := dialTestClient(t, startSSHServer(t, false))

	start := time.Now()
	done := make(chan bool, 1)
	go func() { done <- alive(context.Background(), client) }()

	select {
	case ok := <-done:
		elapsed := time.Since(start)
		if ok {
			t.Fatal("alive() said a silent connection was healthy")
		}
		if elapsed < aliveTimeout/2 {
			t.Fatalf("alive() gave up after %v, far sooner than aliveTimeout %v — "+
				"the connection is probably not actually silent, so this test "+
				"would not catch the hang it exists for", elapsed, aliveTimeout)
		}
	case <-time.After(aliveTimeout + 10*time.Second):
		t.Fatalf("alive() never returned; it is blocking on the keepalive again")
	}
}

func TestAliveHonoursContextCancellation(t *testing.T) {
	client := dialTestClient(t, startSSHServer(t, false))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	done := make(chan bool, 1)
	go func() { done <- alive(ctx, client) }()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("alive() said a silent connection was healthy")
		}
		if elapsed := time.Since(start); elapsed >= aliveTimeout {
			t.Fatalf("alive() waited %v, so it fell through to aliveTimeout "+
				"instead of honouring the caller's deadline", elapsed)
		}
	case <-time.After(aliveTimeout):
		t.Fatal("alive() ignored the cancelled context")
	}
}
