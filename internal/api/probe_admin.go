package api

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

// probeMachineInput is what the panel can change about a probe machine.
// Display carries the public page's fields (price, expiry, group…) whole.
type probeMachineInput struct {
	Name    string          `json:"name"`
	Hidden  bool            `json:"hidden"`
	Display json.RawMessage `json:"display"`
	// NodeID is the panel machine it is, null for none.
	NodeID *int64 `json:"node_id"`
}

func (s *Server) handleUpdateProbeMachine(w http.ResponseWriter, r *http.Request) {
	var in probeMachineInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	var display map[string]any
	if in.Name == "" || len([]rune(in.Name)) > 100 || json.Unmarshal(in.Display, &display) != nil || display == nil {
		writeError(w, http.StatusBadRequest, "名称不能为空，最多 100 个字")
		return
	}
	st := s.svc.Store()
	uuid := chi.URLParam(r, "uuid")
	clients, err := st.ProbeClients(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取探针机器失败")
		return
	}
	i := slices.IndexFunc(clients, func(c store.ProbeClient) bool { return c.UUID == uuid })
	if i < 0 {
		writeError(w, http.StatusNotFound, "这台探针机器不存在")
		return
	}
	if err := st.UpdateProbeClient(r.Context(), store.ProbeClient{UUID: uuid, Name: in.Name, Hidden: in.Hidden, Display: in.Display}); err != nil {
		writeError(w, statusForError(err), "保存失败")
		return
	}
	if !sameNode(clients[i].NodeID, in.NodeID) {
		if err := st.SetProbeNode(r.Context(), uuid, in.NodeID); err != nil {
			writeError(w, statusForError(err), "关联机器失败，那台机器可能已被删除")
			return
		}
	}
	s.audit(r, "probe.machine.update", in.Name, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sameNode(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// handleReorderProbeMachines saves the order of the probe machines that are
// not one of the panel's machines; the others follow the panel's order.
func (s *Server) handleReorderProbeMachines(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UUIDs []string `json:"uuids"`
	}
	if err := decodeJSON(r, &in); err != nil || in.UUIDs == nil {
		writeError(w, http.StatusBadRequest, "uuids must be an array")
		return
	}
	if err := s.svc.Store().ReorderProbeClients(r.Context(), in.UUIDs); err != nil {
		writeError(w, statusForError(err), "保存顺序失败")
		return
	}
	s.audit(r, "probe.machines.reorder", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteProbeMachine(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	if err := s.svc.Store().DeleteProbeClient(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "删除失败")
		return
	}
	s.probe.Forget(id)
	if err := s.probe.ReloadTasks(r.Context()); err != nil {
		s.log.Error("reload probe tasks", "error", err)
	}
	s.audit(r, "probe.machine.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var probeTaskTypes = []string{"icmp", "tcp", "http"}

// validProbeTask checks a latency check before it is saved, and keeps only
// the machines that exist.
func validProbeTask(t *store.ProbeTask, clients []store.ProbeClient) string {
	t.Name, t.Target = strings.TrimSpace(t.Name), strings.TrimSpace(t.Target)
	switch {
	case t.Name == "" || len([]rune(t.Name)) > 100:
		return "名称不能为空，最多 100 个字"
	case !slices.Contains(probeTaskTypes, t.Type):
		return "方式只能是 ICMP、TCP 或 HTTP"
	case t.Target == "" || len(t.Target) > 255 || strings.ContainsAny(t.Target, " \t\r\n"):
		return "目标不能为空，也不能有空格"
	case t.Interval < 5 || t.Interval > 3600:
		return "间隔在 5 到 3600 秒之间"
	}
	known := map[string]bool{}
	for _, c := range clients {
		known[c.UUID] = true
	}
	t.Clients = slices.DeleteFunc(slices.Clone(t.Clients), func(c string) bool { return !known[c] })
	if t.Clients == nil {
		t.Clients = []string{}
	}
	return ""
}

func (s *Server) handleSaveProbeTask(w http.ResponseWriter, r *http.Request) {
	var t store.ProbeTask
	if err := decodeJSON(r, &t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t.ID = 0
	if chi.URLParam(r, "id") != "" {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		t.ID = id
	}
	clients, err := s.svc.Store().ProbeClients(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取探针机器失败")
		return
	}
	if msg := validProbeTask(&t, clients); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if t.ID, err = s.svc.Store().SaveProbeTask(r.Context(), t); err != nil {
		writeError(w, statusForError(err), "保存延迟任务失败")
		return
	}
	if err := s.probe.ReloadTasks(r.Context()); err != nil {
		s.log.Error("reload probe tasks", "error", err)
	}
	s.audit(r, "probe.task.save", t.Name, t.Type+" "+t.Target)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteProbeTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.Store().DeleteProbeTask(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "删除延迟任务失败")
		return
	}
	if err := s.probe.ReloadTasks(r.Context()); err != nil {
		s.log.Error("reload probe tasks", "error", err)
	}
	s.audit(r, "probe.task.delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type probeEnroll struct {
	Key     string `json:"key"`
	Host    string `json:"host"`
	Command string `json:"command"`
}

func (s *Server) handleProbeEnroll(w http.ResponseWriter, r *http.Request) {
	st := s.svc.Store()
	key, err := st.ProbeSetting(r.Context(), probe.DiscoveryKeySetting)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取接入设置失败")
		return
	}
	host, err := st.ProbeSetting(r.Context(), komari.PublicHostSetting)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取接入设置失败")
		return
	}
	out := probeEnroll{Key: key, Host: host}
	if key != "" && host != "" {
		out.Command = "bash <(curl -sL " + service.KomariInstaller + ") -e https://" + host + " --auto-discovery " + key + " --disable-web-ssh --disable-auto-update"
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProbeEnrollKey makes a new registration key; machines already
// registered keep their own tokens.
func (s *Server) handleProbeEnrollKey(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Store().SetProbeSetting(r.Context(), probe.DiscoveryKeySetting, rand.Text()); err != nil {
		writeError(w, http.StatusInternalServerError, "生成注册密钥失败")
		return
	}
	s.audit(r, "probe.enroll.key", "", "")
	s.handleProbeEnroll(w, r)
}

func (s *Server) handleProbeTheme(w http.ResponseWriter, r *http.Request) {
	if s.site == nil {
		writeError(w, http.StatusNotFound, "公开页没有启用")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	file, _, err := r.FormFile("theme")
	if err != nil {
		writeError(w, http.StatusBadRequest, "没有收到主题文件，或文件超过 32MB")
		return
	}
	defer file.Close()
	pkg, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取主题文件失败")
		return
	}
	version, err := s.site.InstallTheme(r.Context(), pkg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "probe.theme", version, "")
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

// handleSetProbeMachineTasks chooses which latency checks one machine runs,
// for the probe page's by-machine view.
func (s *Server) handleSetProbeMachineTasks(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TaskIDs []int64 `json:"task_ids"`
	}
	if err := decodeJSON(r, &in); err != nil || in.TaskIDs == nil {
		writeError(w, http.StatusBadRequest, "task_ids must be an array")
		return
	}
	uuid := chi.URLParam(r, "uuid")
	if err := s.svc.Store().SetProbeClientTasks(r.Context(), uuid, in.TaskIDs); err != nil {
		writeError(w, statusForError(err), "保存失败")
		return
	}
	if err := s.probe.ReloadTasks(r.Context()); err != nil {
		s.log.Error("reload probe tasks", "error", err)
	}
	s.audit(r, "probe.machine.tasks", uuid, strconv.Itoa(len(in.TaskIDs)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
