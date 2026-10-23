package api

import (
	"net/http"
	"strconv"

	"github.com/kukumi1/fluxlite/internal/audit"
)

// trafficAuditPageSize is fixed: the list holds at most audit.Capacity rows,
// so ten pages cover all of it.
const trafficAuditPageSize = 100

type trafficAuditPage struct {
	Items    []audit.Record `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
	Capacity int            `json:"capacity"`
	Machines []string       `json:"machines"`
}

func (s *Server) handleTrafficAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	until, _ := strconv.ParseUint(q.Get("until"), 10, 64)
	items, total := s.svc.TrafficAudit(page, trafficAuditPageSize, audit.Filter{Machine: q.Get("machine"), Query: q.Get("q"), Until: until})
	writeJSON(w, http.StatusOK, trafficAuditPage{
		Items: items, Page: page, PageSize: trafficAuditPageSize, Total: total, Capacity: audit.Capacity,
		Machines: s.svc.TrafficAuditMachines(),
	})
}
