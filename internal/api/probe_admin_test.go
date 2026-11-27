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

// Choosing a machine's latency checks adds it to those and takes it out of
// the rest, leaving the other machines where they were.
func TestSetProbeClientTasks(t *testing.T) {
	_, st := newAuditTestServer(t)
	ctx := context.Background()
	err := st.ReplaceProbe(ctx, store.ProbeImport{
		Clients: []store.ProbeClient{{UUID: "a", Token: "ta", Name: "A"}, {UUID: "b", Token: "tb", Name: "B"}},
		Tasks: []store.ProbeTask{
			{ID: 1, Name: "成都电信", Type: "tcp", Target: "x:1", Interval: 60, Clients: []string{"a", "b"}},
			{ID: 2, Name: "FAC-TW", Type: "tcp", Target: "y:1", Interval: 60, Clients: []string{"b"}},
			{ID: 3, Name: "四川移动", Type: "tcp", Target: "z:1", Interval: 60, Clients: []string{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeClientTasks(ctx, "a", []int64{2, 3}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := st.ProbeTasks(ctx)
	got := map[int64][]string{}
	for _, task := range tasks {
		got[task.ID] = task.Clients
	}
	if len(got[1]) != 1 || got[1][0] != "b" || len(got[2]) != 2 || len(got[3]) != 1 || got[3][0] != "a" {
		t.Fatalf("%v", got)
	}
	if err := st.SetProbeClientTasks(ctx, "gone", nil); err != store.ErrNotFound {
		t.Fatal(err)
	}
}
