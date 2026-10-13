package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// Done signals when the adoption gate's select has been evaluated. Holding the
// gate before starting the request makes these tests deterministic without sleeps.
type adoptionWaitContext struct {
	context.Context
	waiting    chan struct{}
	mayProceed <-chan struct{}
	once       sync.Once
}

func (ctx *adoptionWaitContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.mayProceed:
		return nil
	default:
		return errors.New("adoption accessed storage before acquiring the shared-service gate")
	}
}

func (ctx *adoptionWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func waitAdoptionEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatal("adoption did not reach the gate")
	}
}

func adoptionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("adoption did not return")
		return nil
	}
}

func seedExternalAdoptionUser(t *testing.T, s *Service, nodeID int64, name string, port int) *model.SingBoxUser {
	t.Helper()
	now := time.Now().UTC()
	u := &model.SingBoxUser{
		NodeID: nodeID, Name: name, Protocol: model.SingBoxSS2022, Port: port,
		Source: model.SingBoxExternal, ServiceName: "sing-box", Status: model.SingBoxStatusStopped,
		ConfigPath:     "/etc/sing-box/conf.d/" + name + ".json",
		ExternalPath:   "/etc/sing-box/conf.d/" + name + ".json",
		BaseQuotaBytes: 1, PeriodDays: 30, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour),
	}
	if err := s.store.CreateSingBoxUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAdoptSingBoxUserRechecksSourceAfterConcurrentHandover(t *testing.T) {
	st, s := newTestLookup(t)
	node := seedNode(t, st, "shared")
	u := seedExternalAdoptionUser(t, s, node.ID, "first", 31001)
	// Model the first request's in-flight remote handover by holding the exact
	// production gate; its final database commit happens before the gate opens.
	release, err := s.lockSingBoxAdoption(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	mayProceed := make(chan struct{})
	ctx := &adoptionWaitContext{Context: context.Background(), waiting: make(chan struct{}), mayProceed: mayProceed}
	result := make(chan error, 1)
	// Exercise the public service method, not just the remote handover helper.
	// Pool is nil: any stale read reaching SSH would fail this regression test.
	go func() { result <- s.AdoptSingBoxUser(ctx, u.ID, 100) }()
	waitAdoptionEvent(t, ctx.waiting)
	u.Source, u.Enabled, u.ServiceName = model.SingBoxManaged, true, "fluxlite-singbox-first"
	u.ConfigPath = "/etc/fluxlite/singbox/first/config.json"
	if err := st.UpdateSingBoxUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	close(mayProceed)
	release()
	release = nil
	if err := adoptionResult(t, result); !errors.Is(err, ErrSingBoxNotManaged) {
		t.Fatalf("queued duplicate must reject the newly managed source before SSH: %v", err)
	}
	got, err := st.SingBoxUserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != model.SingBoxManaged || !got.Enabled || got.ServiceName != u.ServiceName || got.ConfigPath != u.ConfigPath {
		t.Fatalf("queued duplicate changed the first successful adoption: %+v", got)
	}
	// A rejection must release the gate too.
	probeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releaseProbe, err := s.lockSingBoxAdoption(probeCtx)
	if err != nil {
		t.Fatalf("rejected duplicate leaked the gate: %v", err)
	}
	releaseProbe()
}

func TestAdoptSingBoxUserSerializesDifferentFragmentsAndCancelsWait(t *testing.T) {
	st, s := newTestLookup(t)
	node := seedNode(t, st, "shared")
	first := seedExternalAdoptionUser(t, s, node.ID, "first", 31001)
	second := seedExternalAdoptionUser(t, s, node.ID, "second", 31002)
	release, err := s.lockSingBoxAdoption(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &adoptionWaitContext{Context: baseCtx, waiting: make(chan struct{}), mayProceed: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- s.AdoptSingBoxUser(ctx, second.ID, 100) }()
	waitAdoptionEvent(t, ctx.waiting)
	// Cancellation must finish while the first fragment still holds the gate,
	// rather than starting a second shared stop/start sequence on the same node.
	cancel()
	if err := adoptionResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued different fragment must respect cancellation before mutation: %v", err)
	}
	for _, want := range []*model.SingBoxUser{first, second} {
		got, err := st.SingBoxUserByID(context.Background(), want.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != model.SingBoxExternal || got.ServiceName != "sing-box" || got.Enabled {
			t.Fatalf("waiting adoption mutated a shared fragment: %+v", got)
		}
	}
}

func TestAdoptSingBoxUserAlreadyCanceledDoesNotAcquireGate(t *testing.T) {
	s := &Service{} // Nil store and pool ensure neither is consulted.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.AdoptSingBoxUser(ctx, 1, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request should not read storage or dial SSH: %v", err)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), time.Second)
	defer cancelProbe()
	release, err := s.lockSingBoxAdoption(probeCtx)
	if err != nil {
		t.Fatalf("canceled request leaked the gate: %v", err)
	}
	release()
}
