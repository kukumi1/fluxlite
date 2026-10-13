package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeConsoleReader struct {
	kind   websocket.MessageType
	data   []byte
	before func()
	err    error
}

func (r fakeConsoleReader) Read(context.Context) (websocket.MessageType, []byte, error) {
	if r.before != nil {
		r.before()
	}
	return r.kind, r.data, r.err
}

func TestConsoleAuthorizationChecksRevocationExpiryAndUnlock(t *testing.T) {
	for _, change := range []string{"delete", "expire", "lock", "database failure"} {
		t.Run(change, func(t *testing.T) {
			srv, st, _, token := recoveryServer(t)
			ctx := context.Background()
			if err := st.MarkConsoleUnlocked(ctx, token, time.Now()); err != nil {
				t.Fatal(err)
			}
			if !srv.consoleSessionAllowed(ctx, token) {
				t.Fatal("live unlocked session rejected")
			}
			var err error
			switch change {
			case "delete":
				err = st.DeleteSession(ctx, token)
			case "expire":
				_, err = st.DB().Exec(`UPDATE sessions SET expires_at=? WHERE token=?`, time.Now().UTC().Add(-time.Minute), token)
			case "lock":
				_, err = st.DB().Exec(`UPDATE sessions SET console_unlocked_at=NULL WHERE token=?`, token)
			case "database failure":
				err = st.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if srv.consoleSessionAllowed(ctx, token) {
				t.Fatal("invalid terminal authorization remained usable")
			}
		})
	}
}

func TestConsoleRevokedInputIsNeverForwarded(t *testing.T) {
	for _, kind := range []websocket.MessageType{websocket.MessageBinary, websocket.MessageText} {
		t.Run(kind.String(), func(t *testing.T) {
			srv, st, _, token := recoveryServer(t)
			ctx := context.Background()
			if err := st.MarkConsoleUnlocked(ctx, token, time.Now()); err != nil {
				t.Fatal(err)
			}
			reader := fakeConsoleReader{kind: kind, data: []byte("command or resize"), before: func() {
				if err := st.DeleteSession(ctx, token); err != nil {
					t.Fatal(err)
				}
			}}
			_, data, err := readAuthorizedConsoleFrame(ctx, reader, func(ctx context.Context) bool {
				return srv.consoleSessionAllowed(ctx, token)
			})
			if !errors.Is(err, errConsoleRevoked) || len(data) != 0 {
				t.Fatal("received frame from revoked session remained forwardable", err)
			}
		})
	}
}

func TestConsoleAuthorizedReadPreservesFrameAndReadErrors(t *testing.T) {
	ctx := context.Background()
	want := []byte("input")
	kind, data, err := readAuthorizedConsoleFrame(ctx, fakeConsoleReader{kind: websocket.MessageBinary, data: want}, func(context.Context) bool { return true })
	if err != nil || kind != websocket.MessageBinary || string(data) != string(want) {
		t.Fatal("valid input changed", err)
	}
	wantErr := errors.New("read error")
	_, data, err = readAuthorizedConsoleFrame(ctx, fakeConsoleReader{err: wantErr}, func(context.Context) bool { t.Fatal("read failure must not validate"); return true })
	if !errors.Is(err, wantErr) || len(data) != 0 {
		t.Fatal("read error was ignored", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, data, err = readAuthorizedConsoleFrame(ctx, fakeConsoleReader{data: want}, func(context.Context) bool { return true })
	if !errors.Is(err, errConsoleRevoked) || len(data) != 0 {
		t.Fatal("cancelled console retained writable input")
	}
}

func TestConsoleIdleAuthorizationWatchCancelsOnRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var allowed atomic.Bool
	allowed.Store(true)
	checked := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchConsoleAuthorization(ctx, cancel, time.Millisecond, func(context.Context) bool {
			select {
			case checked <- struct{}{}:
			default:
			}
			return allowed.Load()
		})
	}()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("authorization watcher never checked")
	}
	if ctx.Err() != nil {
		t.Fatal("valid idle console cancelled")
	}
	allowed.Store(false)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revoked idle console not cancelled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authorization watcher leaked")
	}
}

// A real loopback WebSocket exercises the read cancellation/close path with
// no managed node or SSH connection. Revoking the DB session closes it even
// if the browser sends nothing after revocation.
func TestConsoleRevocationClosesIdleWebSocket(t *testing.T) {
	srv, st, _, token := recoveryServer(t)
	if err := st.MarkConsoleUnlocked(context.Background(), token, time.Now()); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		authorized := func(ctx context.Context) bool { return srv.consoleSessionAllowed(ctx, token) }
		go watchConsoleAuthorization(ctx, cancel, 10*time.Millisecond, authorized)
		go func() { <-ctx.Done(); _ = conn.CloseNow() }()
		close(ready)
		_, _, _ = readAuthorizedConsoleFrame(ctx, conn, authorized)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	<-ready
	if err := st.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("idle socket only closed on timeout, not revocation", err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("socket handler did not terminate")
	}
}
