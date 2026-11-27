package api

import (
	"net/http"
	"time"

	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/store"
)

// probeMachine is a probe machine with its live state for the panel.
type probeMachine struct {
	store.ProbeClient
	Online bool          `json:"online"`
	At     int64         `json:"at"`
	Report *probe.Report `json:"report"`
}

type probeOverview struct {
	Enabled  bool              `json:"enabled"`
	Machines []probeMachine    `json:"machines"`
	Tasks    []store.ProbeTask `json:"tasks"`
}

func (s *Server) handleProbeOverview(w http.ResponseWriter, r *http.Request) {
	out := probeOverview{Machines: []probeMachine{}, Tasks: []store.ProbeTask{}}
	if s.probe == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Enabled = true
	st := s.svc.Store()
	clients, err := st.ProbeClients(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取探针机器失败")
		return
	}
	if out.Tasks, err = st.ProbeTasks(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "读取延迟任务失败")
		return
	}
	live := s.probe.Snapshot(time.Now())
	for _, c := range clients {
		m := probeMachine{ProbeClient: c}
		if l := live[c.UUID]; l.Latest != nil {
			m.Online, m.At = l.Online, l.Latest.At.Unix()
			report := l.Latest.Report
			m.Report = &report
		}
		out.Machines = append(out.Machines, m)
	}
	writeJSON(w, http.StatusOK, out)
}
