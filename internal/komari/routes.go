package komari

import (
	"context"
	"encoding/json"
)

// ReturnRoutes gives each traced machine's return routes, by the panel's
// machine ID, already reduced to what a public page may show.
type ReturnRoutes func(context.Context) (map[int64]any, error)

// SetReturnRoutes gives the page the panel's return-route traces, which the
// theme shows on each machine's card.
func (s *Site) SetReturnRoutes(fn ReturnRoutes) {
	s.mu.Lock()
	s.routes = fn
	s.mu.Unlock()
}

// returnRoutes is fluxlite:getReturnRoutes: the return routes of the
// machines the visitor may see, by probe uuid. Komari has no such call, so
// only a theme that knows the panel asks for it.
func (s *Site) returnRoutes(ctx context.Context, _ json.RawMessage, admin bool) (any, *rpcError) {
	s.mu.Lock()
	fn := s.routes
	s.mu.Unlock()
	out := map[string]any{}
	if fn == nil {
		return out, nil
	}
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	routes, err := fn(ctx)
	if err != nil {
		s.log.Error("read return routes", "error", err)
		return nil, &rpcError{errInternal, "Failed to get return routes"}
	}
	for _, c := range clients {
		if c.NodeID == nil {
			continue
		}
		if r, ok := routes[*c.NodeID]; ok {
			out[c.UUID] = r
		}
	}
	return out, nil
}
