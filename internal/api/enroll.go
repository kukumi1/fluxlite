package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

// handleEnrollScript serves the installer. It carries no secret of its own:
// the token is passed as an argument by the operator, so the script itself is
// safe to fetch anonymously.
func (s *Server) handleEnrollScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(enrollScript)); err != nil {
		s.log.Error("write enroll script", "error", err)
	}
}

// handleUninstallScript serves the teardown counterpart of the installer. It
// needs no token: it grants nothing, and it has to work on a machine the panel
// can no longer reach.
func (s *Server) handleUninstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(uninstallScript)); err != nil {
		s.log.Error("write uninstall script", "error", err)
	}
}

// handleEnrollTicket creates a pending enrollment and returns the command.
func (s *Server) handleEnrollTicket(w http.ResponseWriter, r *http.Request) {
	var req service.EnrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ticket, err := s.svc.CreateEnrollTicket(r.Context(), externalBaseURL(r), req)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "enroll.ticket", req.Name, req.Host)
	writeJSON(w, http.StatusCreated, ticket)
}

// handleReenrollTicket issues an installer command for a machine that has been
// rebuilt, aimed at the node record it already has.
func (s *Server) handleReenrollTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ticket, err := s.svc.CreateReenrollTicket(r.Context(), externalBaseURL(r), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "enroll.reinstall_ticket", strconv.FormatInt(id, 10),
		"重装命令已生成；完成后该节点的主机指纹将被重置")
	writeJSON(w, http.StatusCreated, ticket)
}

// handleEnrollKey hands the installer the public key it should authorise.
func (s *Server) handleEnrollKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.svc.AuthorizedKeyForToken(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(key + "\n")); err != nil {
		s.log.Error("write enroll key", "error", err)
	}
}

// handleEnrollRealm serves the pinned realm build to an enrolling node, so a
// machine with poor GitHub connectivity never has to reach it.
func (s *Server) handleEnrollRealm(w http.ResponseWriter, r *http.Request) {
	arch := r.URL.Query().Get("arch")
	if arch != "amd64" && arch != "arm64" {
		writeError(w, http.StatusBadRequest, "arch must be amd64 or arm64")
		return
	}
	bin, err := s.svc.RealmBinaryForToken(r.Context(), r.URL.Query().Get("token"), arch)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(bin); err != nil {
		s.log.Error("write realm binary", "error", err)
	}
}

// handleEnrollReport completes an enrollment and reports the verdict back to
// the terminal the operator is sitting at.
func (s *Server) handleEnrollReport(w http.ResponseWriter, r *http.Request) {
	var report service.EnrollReport
	if err := decodeJSON(r, &report); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	outcome, err := s.svc.CompleteEnroll(r.Context(), report)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	action := "enroll.complete"
	if outcome.Reinstalled {
		// Worth its own action: this path replaced a node's credentials and
		// dropped its pinned host key, which enroll.complete never does.
		action = "enroll.reinstall_complete"
	}
	s.audit(r, action, outcome.Name, outcome.Detail)
	out := enrollReply{EnrollOutcome: outcome}
	out.AgentEndpoint, out.AgentToken = s.enrollAgent(r.Context(), outcome)
	writeJSON(w, http.StatusOK, out)
}

// enrollReply is the enrollment verdict, plus where the machine's probe
// agent should report, when the panel's probe is on.
type enrollReply struct {
	*service.EnrollOutcome
	AgentEndpoint string `json:"agent_endpoint,omitempty"`
	AgentToken    string `json:"agent_token,omitempty"`
}

// errProbeOff is why a machine cannot be given an agent: without the probe's
// public domain there is nowhere for one to report.
var errProbeOff = errors.New("探针没有启用：先在「探针」页填好公开页域名")

// enrollAgent gives a newly enrolled machine its probe identity, paired with
// it from the start, so the installer can set up the agent and the machine
// shows live readings as soon as it appears. Without the probe nothing is
// returned and the installer skips the agent.
func (s *Server) enrollAgent(ctx context.Context, outcome *service.EnrollOutcome) (endpoint, token string) {
	endpoint, token, err := s.agentIdentity(ctx, outcome.NodeID, outcome.Name)
	if err != nil && !errors.Is(err, errProbeOff) {
		s.log.Error("prepare the enrolled machine's probe agent", "node", outcome.Name, "error", err)
	}
	return endpoint, token
}

// agentIdentity is where a machine's agent reports and the token it uses:
// the identity already paired with the machine, so it keeps its history;
// else the agent already reporting from its address; else a new one.
func (s *Server) agentIdentity(ctx context.Context, nodeID int64, name string) (endpoint, token string, err error) {
	if s.probe == nil {
		return "", "", errProbeOff
	}
	st := s.svc.Store()
	host, err := st.ProbeSetting(ctx, komari.PublicHostSetting)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", errProbeOff
	}
	c, err := st.ProbeClientByNode(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		if c, err = s.agentAt(ctx, nodeID); err == nil && c == nil {
			c = &store.ProbeClient{UUID: uuid.NewString(), Token: rand.Text()[:22], Name: name}
			err = st.CreateProbeClient(ctx, *c)
		}
		if err == nil {
			err = st.SetProbeNode(ctx, c.UUID, &nodeID)
		}
	}
	if err != nil {
		return "", "", err
	}
	return "https://" + host, c.Token, nil
}

// agentAt finds the agent already reporting from the machine's address and
// paired with nothing, so enrolling a machine that has only an agent so far
// gives it SSH and keeps that agent, its history and its public-page
// details. An address two unpaired agents share names neither; nil then.
func (s *Server) agentAt(ctx context.Context, nodeID int64) (*store.ProbeClient, error) {
	st := s.svc.Store()
	node, err := st.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	host := net.ParseIP(node.Host)
	if host == nil {
		return nil, nil
	}
	candidates, err := st.ProbeLinkCandidates(ctx)
	if err != nil {
		return nil, err
	}
	var found *store.ProbeClient
	for i, c := range candidates {
		var info struct {
			IPv4 string `json:"ipv4"`
			IPv6 string `json:"ipv6"`
		}
		if json.Unmarshal(c.Info, &info) != nil {
			continue
		}
		if host.Equal(net.ParseIP(info.IPv4)) || host.Equal(net.ParseIP(info.IPv6)) {
			if found != nil {
				return nil, nil
			}
			found = &candidates[i]
		}
	}
	return found, nil
}

// externalBaseURL reconstructs the address the operator's browser used, so the
// generated command points back at a URL the new node can actually reach.
// Behind a reverse proxy the scheme only survives in X-Forwarded-Proto.
func externalBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.Split(fwd, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host)
}

// handleInstallNodeAgent installs the probe agent on a machine the panel
// reaches over SSH, or reinstalls it with the machine's identity, which also
// repairs an agent started with the wrong address.
func (s *Server) handleInstallNodeAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	node, err := s.svc.Store().NodeByID(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), "机器不存在")
		return
	}
	endpoint, token, err := s.agentIdentity(r.Context(), id, node.Name)
	if err != nil {
		if !errors.Is(err, errProbeOff) {
			s.log.Error("prepare a probe agent", "node", node.Name, "error", err)
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.InstallProbeAgent(r.Context(), id, endpoint, token); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.audit(r, "node.agent_install", node.Name, endpoint)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
