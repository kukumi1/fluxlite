package sshx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"golang.org/x/crypto/ssh"
)

// The peer deliberately withholds one protocol response without executing any
// command. Closing the transport must release every handler before test exit.
func stalledSessionClient(t *testing.T, phase string) (*ssh.Client, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reached, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		server, chans, requests, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		var workers sync.WaitGroup
		workers.Add(1)
		go func() { defer workers.Done(); ssh.DiscardRequests(requests) }()
		for pending := range chans {
			if phase == "open" {
				once.Do(func() { close(reached) })
				continue
			}
			channel, reqs, err := pending.Accept()
			if err != nil {
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer channel.Close()
				if phase == "handshake" {
					if _, err := bufio.NewReader(channel).ReadString('\n'); err != nil {
						return
					}
					once.Do(func() { close(reached) })
				}
				for request := range reqs {
					if request.Type != "exec" {
						_ = request.Reply(false, nil)
						continue
					}
					if phase == "exec" {
						once.Do(func() { close(reached) })
						continue
					}
					if err := request.Reply(true, nil); err != nil {
						return
					}
					once.Do(func() { close(reached) })
					if phase == "write" || phase == "wait" {
						continue
					}
					if _, err := io.Copy(io.Discard, channel); err != nil {
						return
					}
					if phase == "exit" {
						_, _ = channel.Stderr().Write([]byte("private-password-from-stderr"))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{9}))
					} else {
						_, _ = channel.Write([]byte("ok"))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					}
					return
				}
			}()
		}
		workers.Wait()
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("SSH server workers leaked")
		}
	})
	return client, reached, done
}

func TestSessionCancellationBoundsAllBlockingPhases(t *testing.T) {
	for _, operation := range []string{"run", "write"} {
		phases := []string{"open", "exec", "wait"}
		if operation == "write" {
			phases = append(phases, "write")
		}
		for _, phase := range phases {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				client, reached, closed := stalledSessionClient(t, phase)
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
				result := make(chan error, 1)
				started := time.Now()
				go func() {
					if operation == "run" {
						_, err := Run(ctx, client, "secret-command")
						result <- err
					} else {
						// Larger than the SSH channel window: a peer that never reads blocks
						// stdin copying even after accepting the exec request.
						content := []byte("secret-content")
						if phase == "write" {
							content = bytes.Repeat([]byte("x"), 8<<20)
						}
						result <- WriteFile(ctx, client, "/tmp/private-file", content, "0600")
					}
				}()
				select {
				case <-reached:
				case <-time.After(time.Second):
					t.Fatal("peer did not reach stalled phase")
				}
				select {
				case err := <-result:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("expected deadline, got %v", err)
					}
					if time.Since(started) > time.Second {
						t.Fatal("deadline exceeded bound")
					}
					if strings.Contains(err.Error(), "secret") {
						t.Fatal("error exposed operation data")
					}
				case <-time.After(time.Second):
					t.Fatal("SSH operation ignored deadline")
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not close transport/workers")
				}
				if alive(context.Background(), &Client{Client: client}) {
					t.Fatal("canceled pooled connection remained reusable")
				}
			})
		}
	}
}

func TestCompletedSessionDoesNotClosePooledConnection(t *testing.T) {
	client, _, _ := stalledSessionClient(t, "success")
	ctx, cancel := context.WithCancel(context.Background())
	result, err := Run(ctx, client, "echo ok")
	if err != nil || result.Stdout != "ok" {
		t.Fatalf("run: %+v, %v", result, err)
	}
	cancel() // Completion must unregister its cancellation callback.
	result, err = Run(context.Background(), client, "echo again")
	if err != nil || result.Stdout != "ok" {
		t.Fatalf("connection closed after completed request: %v", err)
	}
	if err := WriteFile(context.Background(), client, "/tmp/test", []byte("content"), "0600"); err != nil {
		t.Fatal(err)
	}
}

func TestAlreadyCanceledSessionDoesNotClosePooledConnection(t *testing.T) {
	client, _, _ := stalledSessionClient(t, "success")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, client, "not executed"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := WriteFile(ctx, client, "/tmp/not-written", nil, "0600"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), client, "still healthy"); err != nil {
		t.Fatalf("unused connection closed: %v", err)
	}
}

func TestRunCheckDoesNotExposeCommandOrStderr(t *testing.T) {
	client, _, _ := stalledSessionClient(t, "exit")
	_, err := RunCheck(context.Background(), client, "private-command-password")
	if err == nil || strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "9") {
		t.Fatalf("unsafe exit error: %v", err)
	}
}

type sessionTestSecretOpener struct{}

func (sessionTestSecretOpener) Open(secret []byte) ([]byte, error) { return secret, nil }

func TestJumpForwardingOpenHonorsDeadline(t *testing.T) {
	parent, reached, closed := stalledSessionClient(t, "open")
	dialer := NewDialer(nil, sessionTestSecretOpener{}, nil, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := dialer.dialOne(ctx, &model.Node{Host: "127.0.0.1", SSHPort: 22, SSHUser: "test", AuthType: model.AuthPassword, AuthSecret: []byte("test")}, parent)
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("no forwarding request")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarding request ignored deadline")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("jump transport not closed")
	}
}

func TestOwnedJumpConnectionClosesParentTransport(t *testing.T) {
	parent, _, closed := stalledSessionClient(t, "success")
	local, remote := net.Pipe()
	defer remote.Close()
	conn := &ownedJumpConn{Conn: local, parent: parent}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("parent retained after tunnel cancellation")
	}
}

func TestHandshakeCancellationClosesDirectAndJumpTransports(t *testing.T) {
	for _, jump := range []bool{false, true} {
		name := "direct"
		if jump {
			name = "jump"
		}
		t.Run(name, func(t *testing.T) {
			var parent *ssh.Client
			var reached, closed <-chan struct{}
			node := &model.Node{Host: "127.0.0.1", SSHPort: 22, SSHUser: "test", AuthType: model.AuthPassword, AuthSecret: []byte("test")}
			if jump {
				parent, reached, closed = stalledSessionClient(t, "handshake")
			} else {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				started, done := make(chan struct{}), make(chan struct{})
				reached, closed = started, done
				host, port, err := net.SplitHostPort(listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				node.Host = host
				node.SSHPort, err = strconv.Atoi(port)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					defer close(done)
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					reader := bufio.NewReader(conn)
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
					close(started)
					_, _ = io.Copy(io.Discard, reader) // Consume identification; never send our own.
				}()
				t.Cleanup(func() {
					_ = listener.Close()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("direct handshake peer leaked")
					}
				})
			}
			// A long dial timeout proves manual cancellation, not a socket deadline,
			// releases both kinds of handshake promptly.
			dialer := NewDialer(nil, sessionTestSecretOpener{}, nil, 30*time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := dialer.dialOne(ctx, node, parent); result <- err }()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("handshake did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected canceled, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("handshake ignored cancellation")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("handshake transport retained after cancellation")
			}
		})
	}
}

func TestJumpHandshakeUsesDialTimeoutWithoutCallerDeadline(t *testing.T) {
	parent, _, closed := stalledSessionClient(t, "handshake")
	dialer := NewDialer(nil, sessionTestSecretOpener{}, nil, 100*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, err := dialer.dialOne(context.Background(), &model.Node{Host: "127.0.0.1", SSHPort: 22, SSHUser: "test", AuthType: model.AuthPassword, AuthSecret: []byte("test")}, parent)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tunneled handshake ignored dial timeout")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("jump transport retained")
	}
}
