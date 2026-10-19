package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
	"testing"
)

func TestSubscriptionLinkLifecycle(t *testing.T) {
	s, n := subscriptionTestService(t)
	ctx := context.Background()
	v, token, err := s.createSubscription(ctx, SubscriptionInput{Name: "saved", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, func(ctx context.Context, in SingBoxUserInput) (*model.SingBoxUser, error) {
		hash, secret, e := s.store.SubscriptionTokenMaterial(ctx, in.subscriptionID)
		if e != nil || len(hash) != 32 || len(secret) == 0 {
			t.Fatal("secret not durable before provisioning", e)
		}
		return nil, nil
	}, func(context.Context, *store.Subscription) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, e := s.SubscriptionLink(ctx, v.ID)
		if e != nil || got != token {
			t.Fatal("view rotated token", e)
		}
	}
	restarted := &Service{store: s.store, sealer: s.sealer}
	if got, e := restarted.SubscriptionLink(ctx, v.ID); e != nil || got != token {
		t.Fatal("restart lost token", e)
	}
	hash, secret, err := s.store.SubscriptionTokenMaterial(ctx, v.ID)
	if err != nil || bytes.Contains(secret, []byte(token)) {
		t.Fatal("plaintext stored", err)
	}
	list, err := s.ListSubscriptions(ctx)
	if err != nil || !list[0].LinkAvailable {
		t.Fatal("metadata missing", err)
	}
	data, _ := json.Marshal(list)
	if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte("token_hash")) || bytes.Contains(data, []byte("token_secret")) {
		t.Fatal("list leaked secrets")
	}
	originalSealer := s.sealer
	s.sealer = nil
	if got, e := s.ResetSubscriptionToken(ctx, v.ID); e == nil || got != "" {
		t.Fatal("encryption failure returned token")
	}
	afterHash, afterSecret, _ := s.store.SubscriptionTokenMaterial(ctx, v.ID)
	if !bytes.Equal(hash, afterHash) || !bytes.Equal(secret, afterSecret) {
		t.Fatal("failed encryption mutated record")
	}
	calls := 0
	if _, _, e := s.createSubscription(ctx, SubscriptionInput{Name: "fail", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, func(context.Context, SingBoxUserInput) (*model.SingBoxUser, error) { calls++; return nil, nil }, nil); e == nil || calls != 0 {
		t.Fatal("encryption failure provisioned")
	}
	s.sealer = originalSealer
	next, e := s.ResetSubscriptionToken(ctx, v.ID)
	if e != nil || next == token {
		t.Fatal("reset failed", e)
	}
	if _, e = s.store.SubscriptionByHash(ctx, hash); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("old token still valid", e)
	}
	if got, e := s.SubscriptionLink(ctx, v.ID); e != nil || got != next {
		t.Fatal("reset not recoverable", e)
	}
}

func TestSubscriptionLinkLegacyCorruptAndStale(t *testing.T) {
	s, _ := subscriptionTestService(t)
	ctx := context.Background()
	token := "legacy-token"
	hash := sha256.Sum256([]byte(token))
	v := &store.Subscription{Name: "legacy", Enabled: true, Status: "active"}
	if e := s.store.CreateSubscription(ctx, v, hash[:]); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SubscriptionLink(ctx, v.ID); !errors.Is(e, ErrSubscriptionLinkUnavailable) {
		t.Fatal(e)
	}
	if _, e := s.store.SubscriptionByHash(ctx, hash[:]); e != nil {
		t.Fatal("legacy rotated", e)
	}
	for _, secret := range [][]byte{[]byte("bad"), func() []byte {
		v, e := s.sealer.Seal([]byte("wrong-token"))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}()} {
		if e := s.store.ResetSubscriptionToken(ctx, v.ID, hash[:], secret); e != nil {
			t.Fatal(e)
		}
		if got, e := s.SubscriptionLink(ctx, v.ID); !errors.Is(e, ErrSubscriptionLinkCorrupt) || got != "" {
			t.Fatal("corruption accepted", e)
		}
		got, blob, _ := s.store.SubscriptionTokenMaterial(ctx, v.ID)
		if !bytes.Equal(got, hash[:]) || !bytes.Equal(blob, secret) {
			t.Fatal("corruption auto repaired")
		}
	}
	if e := s.store.ResetSubscriptionToken(ctx, v.ID, hash[:]); e != nil {
		t.Fatal(e)
	}
	_, secret, _ := s.store.SubscriptionTokenMaterial(ctx, v.ID)
	if len(secret) != 0 {
		t.Fatal("hash-only reset retained stale secret")
	}
}
