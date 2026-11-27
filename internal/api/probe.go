package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

// probeMachine is a probe machine with its live state for the panel.
type probeMachine struct {
	store.ProbeClient
	// Notify says whether the machine going offline is sent as a notice.
	Notify bool          `json:"notify"`
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
	notify, err := s.probeNotifyList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取掉线通知设置失败")
		return
	}
	live := s.probe.Snapshot(time.Now())
	for _, c := range clients {
		m := probeMachine{ProbeClient: c, Notify: slices.Contains(notify, c.UUID)}
		if l := live[c.UUID]; l.Latest != nil {
			m.Online, m.At = l.Online, l.Latest.At.Unix()
			report := l.Latest.Report
			m.Report = &report
		}
		out.Machines = append(out.Machines, m)
	}
	writeJSON(w, http.StatusOK, out)
}

// probeSite is the public page's settings.
type probeSite struct {
	PublicHost  string `json:"public_host"`
	SiteName    string `json:"sitename"`
	Description string `json:"description"`
	CustomHead  string `json:"custom_head"`
	CustomBody  string `json:"custom_body"`
	Theme       string `json:"theme"`
}

var probeSiteKeys = []string{komari.PublicHostSetting, komari.SiteNameSetting, komari.DescriptionSetting, komari.CustomHeadSetting, komari.CustomBodySetting}

func (s *Server) readProbeSite(r *http.Request) (probeSite, error) {
	st := s.svc.Store()
	values := map[string]string{}
	for _, key := range append(probeSiteKeys, komari.ThemeSetting) {
		v, err := st.ProbeSetting(r.Context(), key)
		if err != nil {
			return probeSite{}, err
		}
		values[key] = v
	}
	if values[komari.ThemeSetting] == "" {
		values[komari.ThemeSetting] = komari.DefaultTheme
	}
	return probeSite{values[komari.PublicHostSetting], values[komari.SiteNameSetting], values[komari.DescriptionSetting],
		values[komari.CustomHeadSetting], values[komari.CustomBodySetting], values[komari.ThemeSetting]}, nil
}

func (s *Server) handleProbeSite(w http.ResponseWriter, r *http.Request) {
	site, err := s.readProbeSite(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取公开页设置失败")
		return
	}
	writeJSON(w, http.StatusOK, site)
}

// hostPattern is a bare domain name, which is all the public page is
// recognised by.
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (s *Server) handleSaveProbeSite(w http.ResponseWriter, r *http.Request) {
	var in probeSite
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.PublicHost = strings.ToLower(strings.TrimSpace(in.PublicHost))
	if in.PublicHost != "" && !hostPattern.MatchString(in.PublicHost) {
		writeError(w, http.StatusBadRequest, "公开页域名只填域名，比如 status.example.com，不带 https:// 和路径")
		return
	}
	panelHost := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		panelHost = h
	}
	// The panel answers that domain with the public page alone, so setting
	// it to the one in use would lock the operator out.
	if in.PublicHost != "" && strings.EqualFold(in.PublicHost, panelHost) {
		writeError(w, http.StatusBadRequest, "公开页域名不能和你现在打开面板用的域名相同，否则面板会打不开")
		return
	}
	st := s.svc.Store()
	values := []string{in.PublicHost, strings.TrimSpace(in.SiteName), strings.TrimSpace(in.Description), in.CustomHead, in.CustomBody}
	for i, key := range probeSiteKeys {
		if err := st.SetProbeSetting(r.Context(), key, values[i]); err != nil {
			writeError(w, http.StatusInternalServerError, "保存公开页设置失败")
			return
		}
	}
	if s.site != nil {
		s.site.Forget()
	}
	s.audit(r, "probe.site", in.PublicHost, "")
	s.handleProbeSite(w, r)
}

func (s *Server) handleProbeThemeLogin(w http.ResponseWriter, r *http.Request) {
	if s.site == nil {
		writeError(w, http.StatusNotFound, "公开页没有启用")
		return
	}
	url, err := s.site.LoginURL(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "probe.theme_login", "", "")
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// probeNotifyList is the machines whose going offline is sent as a notice.
func (s *Server) probeNotifyList(ctx context.Context) ([]string, error) {
	raw, err := s.svc.Store().ProbeSetting(ctx, probe.OfflineNotifySetting)
	if err != nil || raw == "" {
		return []string{}, err
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Server) handleProbeNotify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Notify bool `json:"notify"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := chi.URLParam(r, "uuid")
	list, err := s.probeNotifyList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取掉线通知设置失败")
		return
	}
	list = slices.DeleteFunc(list, func(c string) bool { return c == id })
	if in.Notify {
		list = append(list, id)
	}
	b, err := json.Marshal(list)
	if err == nil {
		err = s.svc.Store().SetProbeSetting(r.Context(), probe.OfflineNotifySetting, string(b))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存掉线通知设置失败")
		return
	}
	s.audit(r, "probe.notify", id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"notify": in.Notify})
}

// probeStatus sends a probe machine going offline or coming back, when the
// event is on and the machine is one it is sent for.
func (s *Server) probeStatus(ctx context.Context, client string, down bool, lastSeen time.Time, lasted time.Duration) {
	cfg, err := s.svc.AlertSettings(ctx)
	if err != nil || !cfg.Events.ProbeOffline {
		return
	}
	list, err := s.probeNotifyList(ctx)
	if err != nil || !slices.Contains(list, client) {
		return
	}
	clients, err := s.svc.Store().ProbeClients(ctx)
	if err != nil {
		return
	}
	name := client
	for _, c := range clients {
		if c.UUID != client {
			continue
		}
		// A machine the panel also reaches over SSH is reported by the
		// panel's own offline alert; a second notice would only repeat it.
		if c.NodeID != nil {
			return
		}
		name = c.Name
	}
	text := service.ProbeOnlineText(name, lasted)
	if down {
		text = service.ProbeOfflineText(name, lastSeen)
	}
	if err := s.NotifyTelegram(ctx, text); err != nil {
		s.log.Warn("send probe notice", "client", name, "error", err)
	}
}
