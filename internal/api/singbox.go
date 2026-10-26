package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/singbox"
)

func (s *Server) handleScanSingBox(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	discovered, err := s.svc.ScanSingBox(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.scan", strconv.FormatInt(id, 10), "read-only discovery")
	writeJSON(w, http.StatusOK, discovered)
}

func (s *Server) handleListSingBoxUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.svc.ListSingBoxUsers(r.Context())
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateSingBoxUser(w http.ResponseWriter, r *http.Request) {
	var in service.SingBoxUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.svc.CreateSingBoxUser(r.Context(), in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.create", strconv.FormatInt(u.ID, 10), u.Name)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateSingBoxUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req service.SingBoxUserUpdate
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.UpdateSingBoxUser(r.Context(), id, req); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.update", strconv.FormatInt(id, 10), req.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangeSingBoxPort(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Port int `json:"port"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	move, err := s.svc.ChangeSingBoxPort(r.Context(), id, req.Port)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.port", strconv.FormatInt(id, 10), fmt.Sprintf("端口改为 %d", move.Port))
	writeJSON(w, http.StatusOK, move)
}

func (s *Server) handleStartSingBoxUser(w http.ResponseWriter, r *http.Request) {
	s.handleSetSingBoxEnabled(w, r, true)
}

func (s *Server) handleStopSingBoxUser(w http.ResponseWriter, r *http.Request) {
	s.handleSetSingBoxEnabled(w, r, false)
}

func (s *Server) handleSetSingBoxEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.SetSingBoxEnabled(r.Context(), id, enabled); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.set_enabled", strconv.FormatInt(id, 10), strconv.FormatBool(enabled))
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

type singBoxTopUpRequest struct {
	Bytes int64 `json:"bytes"`
}

func (s *Server) handleTopUpSingBoxUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req singBoxTopUpRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.AddSingBoxTopUp(r.Context(), id, req.Bytes); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.top_up", strconv.FormatInt(id, 10), strconv.FormatInt(req.Bytes, 10))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleReduceSingBoxQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req singBoxTopUpRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.ReduceSingBoxQuota(r.Context(), id, req.Bytes); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.reduce_quota", strconv.FormatInt(id, 10), strconv.FormatInt(req.Bytes, 10))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleResetSingBoxUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.ResetSingBoxUsage(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.reset_usage", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleExtendSingBoxExpiry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.SetSingBoxExpiry(r.Context(), id, req.ExpiresAt); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.expiry_set", strconv.FormatInt(id, 10), req.ExpiresAt.UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdoptSingBoxUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Bytes int64 `json:"bytes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.AdoptSingBoxUser(r.Context(), id, req.Bytes); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.adopt", strconv.FormatInt(id, 10), "external fragment migrated to an isolated service")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleExportSingBoxUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	u, err := s.svc.Store().SingBoxUserByID(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	node, err := s.svc.Store().NodeByID(r.Context(), u.NodeID)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	host, family, available, err := singBoxExportAddress(node, r.URL.Query().Get("address_family"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	export, err := s.svc.ExportSingBoxBundle(r.Context(), id, host)
	if err != nil {
		status := statusForError(err)
		if errors.Is(err, singbox.ErrClientExport) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	export["address_family"] = family
	export["available_families"] = available
	if link, ok := export["link"].(string); ok && link != "" {
		if qrURL, err := qrDataURL(link); err == nil {
			export["qr_data_url"] = qrURL
		} else {
			export["qr_error"] = "二维码生成失败，请复制节点链接或客户端配置导入"
		}
	}
	s.audit(r, "singbox.export", strconv.FormatInt(id, 10), "credentials exported")
	writeJSON(w, http.StatusOK, export)
}

func qrDataURL(value string) (string, error) {
	code, err := qr.Encode(value, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}
	code, err = barcode.Scale(code, 256, 256)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, code); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

func (s *Server) handleDeleteSingBoxUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.DeleteSingBoxUser(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Address selection affects client exports only; never mutate the server bind
// address, credentials or an existing node. Primary host remains the default.
func singBoxExportAddress(node *model.Node, requested string) (string, string, []string, error) {
	primary := strings.Trim(strings.TrimSpace(node.Host), "[]")
	v4 := primary
	ip := net.ParseIP(primary)
	if ip != nil && ip.To4() == nil {
		v4 = ""
	}
	v6 := strings.Trim(strings.TrimSpace(node.IPv6DialAddress()), "[]")
	if v6 == "" && ip != nil && ip.To4() == nil {
		v6 = primary
	}
	if parsed := net.ParseIP(v6); parsed == nil || parsed.To4() != nil {
		v6 = ""
	}
	available := []string{}
	if v4 != "" {
		available = append(available, "ipv4")
	}
	if v6 != "" {
		available = append(available, "ipv6")
	}
	family := requested
	if family == "" {
		if v4 != "" {
			family = "ipv4"
		} else {
			family = "ipv6"
		}
	}
	switch family {
	case "ipv4":
		if v4 != "" {
			return v4, family, available, nil
		}
		return "", "", nil, errors.New("该机器没有可导出的 IPv4 主机地址")
	case "ipv6":
		if v6 != "" {
			return v6, family, available, nil
		}
		return "", "", nil, errors.New("该机器没有可导出的 IPv6 地址")
	default:
		return "", "", nil, errors.New("地址类型必须为 ipv4 或 ipv6")
	}
}
