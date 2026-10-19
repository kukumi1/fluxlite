package api

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/subscription"
	"net/http"
	"strconv"
)

func (s *Server) registerSubscriptions(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.recoveryNoStore)
		r.Get("/subscriptions", s.handleSubscriptions)
		r.Get("/subscriptions/candidates", s.handleSubscriptionCandidates)
		r.Post("/subscriptions", s.handleSubscriptions)
		r.Patch("/subscriptions/{id}", s.handleSubscriptionUpdate)
		r.Delete("/subscriptions/{id}", s.handleSubscriptionDelete)
		r.Post("/subscriptions/{id}/reset-token", s.handleSubscriptionReset)
		r.Get("/subscriptions/{id}/preview", s.handleSubscriptionPreview)
	})
}
func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v, e := s.svc.ListSubscriptions(r.Context())
		if e != nil {
			writeError(w, 500, "读取订阅列表失败")
			return
		}
		writeJSON(w, 200, map[string]any{"subscriptions": v})
		return
	}
	var body struct {
		Name    string  `json:"name"`
		UserIDs []int64 `json:"user_ids"`
	}
	var in service.SubscriptionInput
	if e := decodeJSON(r, &body); e != nil {
		writeError(w, 400, "订阅参数无效")
		return
	}
	in.Name, in.UserIDs = body.Name, body.UserIDs
	v, t, e := s.svc.CreateSubscription(r.Context(), in)
	if e != nil {
		if errors.Is(e, store.ErrConflict) {
			writeError(w, 409, "节点不存在、不可管理或已被其他订阅占用")
			return
		}
		if v != nil {
			s.audit(r, "subscription.create_failed", strconv.FormatInt(v.ID, 10), "retained state may require cleanup")
		}
		writeError(w, 400, "订阅创建失败，请检查机器、协议和保留的失败记录")
		return
	}
	s.audit(r, "subscription.create", strconv.FormatInt(v.ID, 10), "")
	writeJSON(w, 201, map[string]any{"subscription": v, "token": t})
}
func (s *Server) handleSubscriptionUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p service.SubscriptionPatch
	if e := decodeJSON(r, &p); e != nil {
		writeError(w, 400, "订阅更新参数无效")
		return
	}
	v, e := s.svc.UpdateSubscription(r.Context(), id, p)
	if e != nil {
		if errors.Is(e, service.ErrSubscriptionPolicy) {
			writeError(w, 403, "订阅仅管理链接；请在节点页面调整节点策略")
			return
		}
		if v != nil {
			s.audit(r, "subscription.update_incomplete", strconv.FormatInt(id, 10), "inspect subscription state")
		}
		writeError(w, 400, "订阅更新未完成或被额度、到期策略拒绝，请刷新检查")
		return
	}
	s.audit(r, "subscription.update", strconv.FormatInt(id, 10), "")
	writeJSON(w, 200, v)
}
func (s *Server) handleSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if e := s.svc.DeleteSubscription(r.Context(), id); e != nil {
		s.audit(r, "subscription.delete_incomplete", strconv.FormatInt(id, 10), "retained members require retry")
		writeError(w, 409, "订阅删除未完成，保留的专属节点需要重试清理")
		return
	}
	s.audit(r, "subscription.delete", strconv.FormatInt(id, 10), "")
	w.WriteHeader(204)
}
func (s *Server) handleSubscriptionReset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, e := s.svc.ResetSubscriptionToken(r.Context(), id)
	if e != nil {
		writeError(w, 404, "订阅不存在或不可用")
		return
	}
	s.audit(r, "subscription.reset-token", strconv.FormatInt(id, 10), "")
	writeJSON(w, 200, map[string]string{"token": t})
}
func (s *Server) handleSubscriptionPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.renderSubscription(w, r, id, "")
}
func (s *Server) handleSubscriptionDownload(w http.ResponseWriter, r *http.Request) {
	s.renderSubscription(w, r, 0, chi.URLParam(r, "token"))
}
func (s *Server) renderSubscription(w http.ResponseWriter, r *http.Request, id int64, token string) {
	w.Header().Set("Cache-Control", "no-store")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "base64"
	}
	v, e := s.svc.RenderSubscription(r.Context(), id, token, format)
	if e != nil && !(id != 0 && errors.Is(e, subscription.ErrNoCompatible)) {
		if errors.Is(e, store.ErrNotFound) {
			writeError(w, 404, "订阅不存在或不可用")
		} else {
			writeError(w, 422, "订阅格式不支持或没有兼容节点")
		}
		return
	}
	if id == 0 && len(v.Content) == 0 {
		writeError(w, 422, "没有兼容节点")
		return
	}
	if id != 0 {
		writeJSON(w, 200, map[string]any{"content": string(v.Content), "content_type": v.ContentType, "unsupported": v.Unsupported, "included_count": v.IncludedCount})
		return
	}
	w.Header().Set("Content-Type", v.ContentType)
	w.Header().Set("X-Subscription-Unsupported-Count", strconv.Itoa(len(v.Unsupported)))
	w.WriteHeader(200)
	_, _ = w.Write(v.Content)
}

func (s *Server) handleSubscriptionLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	token, err := s.svc.SubscriptionLink(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, 404, "订阅不存在或不可用")
		case errors.Is(err, service.ErrSubscriptionLinkUnavailable):
			writeJSON(w, 409, map[string]string{"error": "旧订阅未保存可恢复链接，请手动重置一次；旧地址不会自动改变", "code": "subscription_link_unavailable"})
		default:
			writeError(w, 500, "订阅链接读取失败，请检查加密密钥和数据完整性")
		}
		return
	}
	s.audit(r, "subscription.link_view", strconv.FormatInt(id, 10), "")
	writeJSON(w, 200, map[string]string{"token": token})
}

func (s *Server) handleSubscriptionCandidates(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.svc.SubscriptionCandidates(r.Context())
	if err != nil {
		writeError(w, 500, "读取节点失败")
		return
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes})
}
