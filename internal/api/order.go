package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/kukumi1/fluxlite/internal/store"
)

func (s *Server) handleReorderNodes(w http.ResponseWriter, r *http.Request) {
	s.handleReorder(w, r, "nodes.reorder", s.svc.Store().ReorderNodes)
}

func (s *Server) handleReorderRoutes(w http.ResponseWriter, r *http.Request) {
	s.handleReorder(w, r, "routes.reorder", s.svc.Store().ReorderRoutes)
}

func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request, action string, reorder func(context.Context, []int64) error) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.IDs == nil {
		writeError(w, http.StatusBadRequest, "ids must be an array")
		return
	}
	if err := reorder(r.Context(), in.IDs); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, store.ErrInvalidOrder):
			status = http.StatusBadRequest
		case errors.Is(err, store.ErrConflict):
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	s.audit(r, action, "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
