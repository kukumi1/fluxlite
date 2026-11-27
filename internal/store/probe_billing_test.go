package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// A machine set to renew by itself is moved a whole period at a time past
// now; one that does not renew keeps the date it was given.
func TestAdvanceProbeRenewals(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	display := func(v map[string]any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	err = st.ReplaceProbe(ctx, ProbeImport{Clients: []ProbeClient{
		{UUID: "monthly", Token: "a", Name: "M", Display: display(map[string]any{"expired_at": "2026-12-02 00:00:00+00:00", "billing_cycle": 30, "auto_renewal": 1})},
		{UUID: "yearly", Token: "b", Name: "Y", Display: display(map[string]any{"expired_at": "2025-04-22T00:00:00.000Z", "billing_cycle": 365, "auto_renewal": true})},
		{UUID: "manual", Token: "c", Name: "N", Display: display(map[string]any{"expired_at": "2026-12-02 00:00:00+00:00", "billing_cycle": 30, "auto_renewal": 0})},
		{UUID: "later", Token: "d", Name: "L", Display: display(map[string]any{"expired_at": "2027-01-01 00:00:00+00:00", "billing_cycle": 30, "auto_renewal": 1})},
		{UUID: "none", Token: "e", Name: "X", Display: display(map[string]any{"price": 5})},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 10, 8, 0, 0, 0, time.UTC)
	n, err := st.AdvanceProbeRenewals(ctx, now)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	clients, _ := st.ProbeClients(ctx)
	got := map[string]string{}
	for _, c := range clients {
		if b, ok := BillingOf(c.Display); ok {
			got[c.UUID] = b.ExpiresAt.Format("2006-01-02")
		}
	}
	want := map[string]string{"monthly": "2027-01-02", "yearly": "2027-04-22", "manual": "2026-12-02", "later": "2027-01-01"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
	if _, ok := got["none"]; ok {
		t.Error("a machine without an expiry has none")
	}
	if n, _ := st.AdvanceProbeRenewals(ctx, now); n != 0 {
		t.Fatal("a renewed machine is not renewed again")
	}
}
