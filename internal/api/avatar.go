package api

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"strings"
)

const (
	// avatarMaxBytes caps what lands in the database. The browser downsizes to
	// 128px before uploading, which lands far under this; the cap is here
	// because the browser is not the only thing that can call this endpoint.
	avatarMaxBytes = 256 << 10

	// avatarMaxPixels bounds the decoded image. A small PNG can decode to an
	// enormous bitmap, so the byte cap alone does not bound the work.
	avatarMaxPixels = 512
)

const avatarDataURLPrefix = "data:image/png;base64,"

type avatarRequest struct {
	Data string `json:"data"`
}

// decodeAvatar turns the submitted data URL into stored bytes.
//
// It re-decodes the image rather than trusting the declared type. The browser
// already re-encodes through a canvas, which strips metadata and anything
// executable, but a request does not have to come from the browser — so the
// server establishes for itself that these bytes are a PNG of a sane size,
// and stores only what it managed to decode.
func decodeAvatar(dataURL string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(dataURL, avatarDataURLPrefix)
	if !ok {
		return nil, fmt.Errorf("头像必须是 PNG data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("头像数据无法解码：%w", err)
	}
	if len(raw) > avatarMaxBytes {
		return nil, fmt.Errorf("头像超过 %d KB", avatarMaxBytes>>10)
	}

	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("头像不是有效的 PNG：%w", err)
	}
	if config.Width > avatarMaxPixels || config.Height > avatarMaxPixels {
		return nil, fmt.Errorf("头像尺寸不能超过 %dx%d", avatarMaxPixels, avatarMaxPixels)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("头像无法解析：%w", err)
	}
	return raw, nil
}

// avatarDataURL renders stored bytes for the client, or empty when unset.
func avatarDataURL(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	return avatarDataURLPrefix + base64.StdEncoding.EncodeToString(raw)
}

func (s *Server) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	var req avatarRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := decodeAvatar(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user := userFrom(r.Context())
	if err := s.svc.Store().SetUserAvatar(r.Context(), user.ID, raw); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "account.avatar_set", user.Username, fmt.Sprintf("%d bytes", len(raw)))
	writeJSON(w, http.StatusOK, map[string]string{"avatar": avatarDataURL(raw)})
}

func (s *Server) handleClearAvatar(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if err := s.svc.Store().ClearUserAvatar(r.Context(), user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "account.avatar_clear", user.Username, "")
	writeJSON(w, http.StatusOK, map[string]string{"avatar": ""})
}
