package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
)

func TestCreateSingBoxRollback(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup succeeds", true: "cleanup fails"}[cleanupFails], func(t *testing.T) {
			st, svc := newTestLookup(t)
			node := seedNode(t, st, "create")
			sealer, err := cryptox.NewSealer(bytes.Repeat([]byte{7}, cryptox.MasterKeySize))
			if err != nil {
				t.Fatal(err)
			}
			svc.sealer = sealer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deployErr, cleanupErr := errors.New("deploy password=secret"), errors.New("cleanup private-key=secret")
			var cleanupCalled bool
			var deployedID int64
			ops := singBoxCreateOps{
				ports: func(context.Context, *model.Node) (map[int]bool, error) { return map[int]bool{10000: true}, nil },
				deploy: func(ctx context.Context, n *model.Node, u *model.SingBoxUser, b *singbox.Bundle) error {
					deployedID = u.ID
					if u.Port != 10001 {
						t.Errorf("live occupied port selected: %d", u.Port)
					}
					cancel() // Partial deployment plus an abandoned request.
					return deployErr
				},
				cleanup: func(ctx context.Context, n *model.Node, u *model.SingBoxUser) error {
					cleanupCalled = true
					if ctx.Err() != nil {
						t.Errorf("cleanup inherited cancellation: %v", ctx.Err())
					}
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 46*time.Second {
						t.Error("cleanup not bounded")
					}
					if cleanupFails {
						return cleanupErr
					}
					return nil
				},
			}
			returned, err := svc.createSingBoxUser(ctx, SingBoxUserInput{NodeID: node.ID, Name: "new", Protocol: model.SingBoxSS2022}, ops)
			if !errors.Is(err, deployErr) || !cleanupCalled {
				t.Fatalf("missing failure/cleanup: %v", err)
			}
			if (returned != nil) != cleanupFails {
				t.Fatalf("retained record response mismatch: %+v", returned)
			}
			if cleanupFails && !errors.Is(err, cleanupErr) {
				t.Fatal("cleanup cause not retained")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed credentials")
			}
			users, err := st.ListSingBoxUsers(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := st.PortClaimed(context.Background(), node.ID, 10001, 0)
			if err != nil {
				t.Fatal(err)
			}
			if cleanupFails {
				if len(users) != 1 || users[0].ID != deployedID || users[0].Enabled || users[0].Status != model.SingBoxStatusError || !claimed {
					t.Fatalf("reservation lost: %+v, claimed=%v", users, claimed)
				}
			} else if len(users) != 0 || claimed {
				t.Fatal("successful cleanup did not release reservation")
			}
		})
	}
}

func TestRollbackBeforeRemoteWritesNeverCleansRemote(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	removed := false
	cause := errors.New("prepare")
	err := rollbackSingBoxCreation(ctx, cause, false, func(context.Context) error { t.Fatal("remote cleanup before writes"); return nil }, func(ctx context.Context) error { removed = true; return ctx.Err() }, func(context.Context) error { t.Fatal("unexpected retain"); return nil })
	if !removed || !errors.Is(err, cause) {
		t.Fatalf("rollback failed: %v", err)
	}
}

func TestCreateSingBoxConflictingInsertDoesNotCleanExistingUser(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "duplicate")
	seedExportBundle(t, svc, node.ID, &singbox.Bundle{Protocol: model.SingBoxSS2022, Port: 11000}, "duplicate")
	ops := singBoxCreateOps{
		ports: func(context.Context, *model.Node) (map[int]bool, error) { return nil, nil },
		deploy: func(context.Context, *model.Node, *model.SingBoxUser, *singbox.Bundle) error {
			t.Fatal("deployed after failed insert")
			return nil
		},
		cleanup: func(context.Context, *model.Node, *model.SingBoxUser) error {
			t.Fatal("cleaned another user's resources")
			return nil
		},
	}
	_, err := svc.createSingBoxUser(context.Background(), SingBoxUserInput{NodeID: node.ID, Name: "duplicate", Protocol: model.SingBoxSS2022, Port: 11001}, ops)
	if err == nil {
		t.Fatal("duplicate name accepted")
	}
	users, err := st.ListSingBoxUsers(context.Background())
	if err != nil || len(users) != 1 || users[0].Port != 11000 {
		t.Fatalf("existing reservation changed: %v %+v", err, users)
	}
}

func TestSelectSingBoxPort(t *testing.T) {
	node := &model.Node{SSHPort: 10000, PortStart: 10000, PortEnd: 10004}
	claimed := func(_ context.Context, p int) (bool, error) { return p == 10003, nil }
	used := map[int]bool{10001: true, 10002: true} // Socket and NAT claims.
	p, err := selectSingBoxPort(context.Background(), node, 0, used, claimed)
	if err != nil || p != 10004 {
		t.Fatalf("selected %d: %v", p, err)
	}
	for _, p := range []int{10000, 10001, 10002, 10003, -1, 65536} {
		if _, err := selectSingBoxPort(context.Background(), node, p, used, claimed); err == nil {
			t.Errorf("accepted unavailable port %d", p)
		}
	}
}

func TestSingBoxRemovalIsScopedAndStrict(t *testing.T) {
	for _, init := range []model.InitSystem{model.InitSystemd, model.InitOpenRC} {
		u := &model.SingBoxUser{ID: 12, Source: model.SingBoxManaged, ServiceName: "fluxlite-singbox-12"}
		cmd, err := singBoxRemovalCommand(init, u)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(cmd, "|| true") || !strings.Contains(cmd, "fluxlite-sb:12:") || !strings.Contains(cmd, "remaining=") {
			t.Fatalf("cleanup silently ignores failures: %s", cmd)
		}
		u.ServiceName = "sing-box"
		if _, err := singBoxRemovalCommand(init, u); err == nil {
			t.Fatal("allowed shared service cleanup")
		}
		u.ServiceName = "fluxlite-singbox-12"
		u.Source = model.SingBoxExternal
		if _, err := singBoxRemovalCommand(init, u); err == nil {
			t.Fatal("allowed external cleanup")
		}
	}
}

func TestConcurrentSingBoxCreatesReserveDifferentPorts(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "concurrent")
	var err error
	svc.sealer, err = cryptox.NewSealer(bytes.Repeat([]byte{7}, cryptox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	ops := singBoxCreateOps{
		ports:   func(context.Context, *model.Node) (map[int]bool, error) { return nil, nil },
		deploy:  func(context.Context, *model.Node, *model.SingBoxUser, *singbox.Bundle) error { return nil },
		cleanup: func(context.Context, *model.Node, *model.SingBoxUser) error { return errors.New("unexpected cleanup") },
	}
	type result struct {
		user *model.SingBoxUser
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, name := range []string{"one", "two"} {
		go func(name string) {
			<-start
			u, err := svc.createSingBoxUser(context.Background(), SingBoxUserInput{NodeID: node.ID, Name: name, Protocol: model.SingBoxSS2022}, ops)
			results <- result{u, err}
		}(name)
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("create errors: %v, %v", first.err, second.err)
	}
	if first.user.Port == second.user.Port || !first.user.Enabled || !second.user.Enabled {
		t.Fatalf("bad allocations: %+v %+v", first.user, second.user)
	}
}

func TestRollbackRetainsReservationWhenDatabaseDeleteFails(t *testing.T) {
	original, dbErr := errors.New("deploy"), errors.New("delete")
	retained := false
	err := rollbackSingBoxCreation(context.Background(), original, true,
		func(context.Context) error { return nil },
		func(context.Context) error { return dbErr },
		func(context.Context) error { retained = true; return nil })
	if !retained || !errors.Is(err, original) || !errors.Is(err, dbErr) {
		t.Fatalf("lost rollback failure: %v", err)
	}
}
