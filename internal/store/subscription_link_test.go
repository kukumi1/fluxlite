package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
)

func TestSubscriptionLinkMigrationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "saved.db")
	st, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	var version int
	if e = st.DB().QueryRow(`SELECT version FROM schema_version`).Scan(&version); e != nil || version != len(migrations) {
		t.Fatal("migration version", version, e)
	}
	hash := sha256.Sum256([]byte("old-credential"))
	v := &Subscription{Name: "legacy", Enabled: true, Status: "active"}
	if e = st.CreateSubscription(ctx, v, hash[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`ALTER TABLE subscriptions DROP COLUMN token_secret`); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`UPDATE schema_version SET version=47`); e != nil {
		t.Fatal(e)
	}
	st.Close()
	st, e = Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	got, secret, e := st.SubscriptionTokenMaterial(ctx, v.ID)
	if e != nil || !bytes.Equal(got, hash[:]) || secret != nil {
		t.Fatal("migration changed legacy", e)
	}
	encrypted := []byte("opaque encrypted material")
	if e = st.ResetSubscriptionToken(ctx, v.ID, hash[:], encrypted); e != nil {
		t.Fatal(e)
	}
	st.Close()
	st, e = Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	got, secret, e = st.SubscriptionTokenMaterial(ctx, v.ID)
	if e != nil || !bytes.Equal(got, hash[:]) || !bytes.Equal(secret, encrypted) {
		t.Fatal("reopen lost credential", e)
	}
}
