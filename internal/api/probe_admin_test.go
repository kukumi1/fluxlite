package api

import (
	"context"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

func TestValidProbeTask(t *testing.T) {
	clients := []store.ProbeClient{{UUID: "u-1"}, {UUID: "u-2"}}
	ok := store.ProbeTask{Name: " 成都联通 ", Type: "tcp", Target: "192.0.2.1:443", Interval: 60, Clients: []string{"u-1", "gone"}}
	if msg := validProbeTask(&ok, clients); msg != "" || ok.Name != "成都联通" || len(ok.Clients) != 1 {
		t.Fatalf("%q %+v", msg, ok)
	}
	for name, task := range map[string]store.ProbeTask{
		"no name":       {Type: "tcp", Target: "x", Interval: 60},
		"unknown type":  {Name: "a", Type: "udp", Target: "x", Interval: 60},
		"spaced target": {Name: "a", Type: "icmp", Target: "a b", Interval: 60},
		"too often":     {Name: "a", Type: "icmp", Target: "x", Interval: 1},
	} {
		if validProbeTask(&task, clients) == "" {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestDeleteProbeClientTakesItsHistory(t *testing.T) {
	_, st := newAuditTestServer(t)
	ctx := context.Background()
	if err := st.ReplaceProbe(ctx, store.ProbeImport{Clients: []store.ProbeClient{{UUID: "u-1", Token: "t-1", Name: "HK"}}}); err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().Unix() / 60 * 60
	if err := st.AddProbeSamples(ctx, []store.ProbeSample{{Client: "u-1", Bucket: bucket, N: 1, CPU: 5}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProbeClient(ctx, "u-1"); err != nil {
		t.Fatal(err)
	}
	if samples, _ := st.ProbeSamples(ctx, "u-1", store.ProbeMinute, 0); len(samples) != 0 {
		t.Fatal("the history goes with the machine")
	}
	if _, err := st.ProbeClientByToken(ctx, "t-1"); err != store.ErrNotFound {
		t.Fatal("its token stops working")
	}
	if err := st.DeleteProbeClient(ctx, "u-1"); err != store.ErrNotFound {
		t.Fatal(err)
	}
}
