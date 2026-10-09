package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
)

type SingBoxUserInput struct {
	NodeID     int64                 `json:"node_id"`
	Name       string                `json:"name"`
	Protocol   model.SingBoxProtocol `json:"protocol"`
	Port       int                   `json:"port"`
	QuotaBytes int64                 `json:"quota_bytes"`
	Cipher     string                `json:"cipher"`
	AccessDays int                   `json:"access_days"`
	ExpiresAt  time.Time             `json:"expires_at"`
}

type SingBoxUserView struct {
	*model.SingBoxUser
	NodeName string `json:"node_name"`
}

type SingBoxDiscovery struct {
	NodeID   int64                 `json:"node_id"`
	Path     string                `json:"path"`
	Tag      string                `json:"tag"`
	Protocol model.SingBoxProtocol `json:"protocol"`
	Port     int                   `json:"port"`
	Running  bool                  `json:"running"`
}

var ErrSingBoxNotManaged = errors.New("this sing-box user is not panel-managed")
var ErrSingBoxQuotaExhausted = errors.New("sing-box user quota is exhausted; add quota or reset usage first")
var ErrSingBoxExpired = errors.New("sing-box user connection validity has expired")

func (s *Service) SetSingBoxExpiry(ctx context.Context, id int64, expiresAt time.Time) error {
	expiresAt = expiresAt.UTC()
	if !expiresAt.IsZero() && !expiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("connection expiry must be in the future")
	}
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	u.ExpiresAt = expiresAt
	if !u.Enabled {
		node, err := s.store.NodeByID(ctx, u.NodeID)
		if err != nil {
			return err
		}
		if err := s.remoteUnit(ctx, node, u, "start"); err != nil {
			return err
		}
		u.Enabled, u.Status = true, model.SingBoxStatusRunning
	}
	return s.store.UpdateSingBoxUser(ctx, u)
}

func (s *Service) ListSingBoxUsers(ctx context.Context) ([]*SingBoxUserView, error) {
	users, err := s.store.ListSingBoxUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*SingBoxUserView, 0, len(users))
	for _, u := range users {
		n, err := s.store.NodeByID(ctx, u.NodeID)
		if err != nil {
			return nil, err
		}
		copy := *u
		copy.ConfigBlob = nil
		out = append(out, &SingBoxUserView{SingBoxUser: &copy, NodeName: n.Name})
	}
	return out, nil
}

// ScanSingBox discovers the standalone installer's fragments without writing
// to them. Discovery deliberately returns protocol/port metadata only; the
// source JSON remains on the node until an explicit adoption workflow exists.
func (s *Service) ScanSingBox(ctx context.Context, nodeID int64) ([]SingBoxDiscovery, error) {
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return nil, err
	}
	res, err := sshx.Run(ctx, client.Client, `for f in /etc/sing-box/conf.d/*.json; do [ -f "$f" ] || continue; printf 'FILE:%s\n' "$f"; cat "$f"; printf '\nEND\n'; done; if command -v systemctl >/dev/null 2>&1 && systemctl is-active sing-box >/dev/null 2>&1; then echo SERVICE:running; elif pgrep -x sing-box >/dev/null 2>&1; then echo SERVICE:running; else echo SERVICE:stopped; fi`)
	if err != nil {
		return nil, err
	}
	serviceRunning := strings.Contains(res.Stdout, "SERVICE:running")
	var out []SingBoxDiscovery
	for _, part := range strings.Split(res.Stdout, "\nEND\n") {
		line := strings.TrimSpace(part)
		if !strings.HasPrefix(line, "FILE:") {
			continue
		}
		lines := strings.SplitN(line, "\n", 2)
		if len(lines) != 2 {
			continue
		}
		path, raw := strings.TrimPrefix(lines[0], "FILE:"), lines[1]
		var cfg struct {
			Inbounds []struct {
				Type string `json:"type"`
				Tag  string `json:"tag"`
				Port int    `json:"listen_port"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue
		}
		for _, in := range cfg.Inbounds {
			protocol := model.SingBoxProtocol(in.Type)
			switch in.Type {
			case "shadowsocks":
				protocol = model.SingBoxSS2022
			case "vless":
				protocol = model.SingBoxVLESSReality
			case "anytls", "hysteria2", "tuic", "trojan", "vmess":
			}
			if !protocol.Valid() || in.Port == 0 {
				continue
			}
			d := SingBoxDiscovery{NodeID: nodeID, Path: path, Tag: in.Tag, Protocol: protocol, Port: in.Port, Running: serviceRunning}
			out = append(out, d)
			if _, err := s.store.SingBoxUserByNodePort(ctx, nodeID, in.Port); errors.Is(err, store.ErrNotFound) {
				now := time.Now().UTC()
				external := &model.SingBoxUser{NodeID: nodeID, Name: in.Tag, Protocol: protocol, Port: in.Port,
					Enabled: serviceRunning, Source: model.SingBoxExternal,
					Status: model.SingBoxStatusUnknown, ServiceName: "sing-box", ConfigPath: path,
					ExternalPath: path, BaseQuotaBytes: 1, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour)}
				if serviceRunning {
					external.Status = model.SingBoxStatusRunning
				} else {
					external.Status = model.SingBoxStatusStopped
				}
				external.ExpiresAt = now.Add(365 * 24 * time.Hour)
				if err := s.store.CreateSingBoxUser(ctx, external); err != nil {
					s.log.Debug("store external sing-box discovery", "path", path, "error", err)
				}
			}
		}
	}
	return out, nil
}

func (s *Service) CreateSingBoxUser(ctx context.Context, in SingBoxUserInput) (*model.SingBoxUser, error) {
	node, err := s.store.NodeByID(ctx, in.NodeID)
	if err != nil {
		return nil, err
	}
	if in.QuotaBytes < 0 {
		return nil, model.ErrSingBoxQuota
	}
	expiresAt := in.ExpiresAt.UTC()
	if expiresAt.IsZero() && in.AccessDays > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(in.AccessDays) * 24 * time.Hour)
	}
	if !expiresAt.After(time.Now().UTC()) {
		return nil, fmt.Errorf("connection expiry must be in the future")
	}
	port := in.Port
	if port == 0 {
		for candidate := node.PortStart; candidate <= node.PortEnd; candidate++ {
			claimed, err := s.store.PortClaimed(ctx, node.ID, candidate, 0)
			if err != nil {
				return nil, err
			}
			if !claimed {
				port = candidate
				break
			}
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("no free port on node %s", node.Name)
	}
	claimed, err := s.store.PortClaimed(ctx, node.ID, port, 0)
	if err != nil {
		return nil, err
	}
	if claimed {
		return nil, fmt.Errorf("port %d is already claimed on %s", port, node.Name)
	}

	periodStart := time.Now().UTC()
	u := &model.SingBoxUser{
		NodeID: node.ID, Name: strings.TrimSpace(in.Name), Protocol: in.Protocol,
		Port: port, Enabled: true, Source: model.SingBoxManaged,
		Status: model.SingBoxStatusUnknown, BaseQuotaBytes: in.QuotaBytes,
		PeriodStartedAt: periodStart, PeriodEndsAt: periodStart.Add(30 * 24 * time.Hour), ExpiresAt: expiresAt,
		ServiceName: "pending", ConfigPath: "pending",
	}
	if err := u.Validate(); err != nil {
		return nil, err
	}
	bundle, err := singbox.Generate(u.Protocol, u.Name, u.Port, "/etc/fluxlite/singbox/pending", in.Cipher)
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		return nil, err
	}
	u.ConfigBlob = sealed
	if err := s.store.CreateSingBoxUser(ctx, u); err != nil {
		return nil, err
	}
	u.ServiceName = fmt.Sprintf("fluxlite-singbox-%d", u.ID)
	u.ConfigPath = fmt.Sprintf("/etc/fluxlite/singbox/%d/config.json", u.ID)
	bundle, err = singbox.Generate(u.Protocol, u.Name, u.Port, fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID), in.Cipher)
	if err != nil {
		_ = s.store.DeleteSingBoxUser(ctx, u.ID)
		return nil, err
	}
	u.ConfigBlob, err = s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		_ = s.store.DeleteSingBoxUser(ctx, u.ID)
		return nil, err
	}
	if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
		_ = s.store.DeleteSingBoxUser(ctx, u.ID)
		return nil, err
	}
	if err := s.deploySingBoxUser(ctx, node, u, bundle); err != nil {
		_ = s.store.DeleteSingBoxUser(ctx, u.ID)
		return nil, err
	}
	u.Status = model.SingBoxStatusRunning
	_ = s.store.UpdateSingBoxUser(ctx, u)
	return u, nil
}

// AdoptSingBoxUser migrates one discovered fragment out of the shared
// installer service into the per-user namespace. The shared service is stopped
// only for the short port handover; on every failure the original fragment is
// restored and the shared service is started again.
func (s *Service) AdoptSingBoxUser(ctx context.Context, id int64, quota int64) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxExternal {
		return ErrSingBoxNotManaged
	}
	if quota <= 0 {
		return model.ErrSingBoxQuota
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return err
	}
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	res, err := sshx.Run(ctx, client.Client, "cat "+shellQuote(u.ExternalPath))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("read external sing-box fragment: %s", strings.TrimSpace(res.Stderr))
	}
	old := *u
	now := time.Now().UTC()
	u.Source = model.SingBoxManaged
	u.Enabled = true
	u.Status = model.SingBoxStatusUnknown
	u.ServiceName = fmt.Sprintf("fluxlite-singbox-%d", u.ID)
	u.ConfigPath = fmt.Sprintf("/etc/fluxlite/singbox/%d/config.json", u.ID)
	u.BaseQuotaBytes = quota
	u.TopUpBytes, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut = 0, 0, 0, 0, 0
	u.PeriodStartedAt, u.PeriodEndsAt = now, now.Add(30*24*time.Hour)
	u.ExpiresAt = now.Add(365 * 24 * time.Hour)
	bundle := &singbox.Bundle{Config: json.RawMessage(res.Stdout), Protocol: u.Protocol, Name: u.Name, Port: u.Port}
	u.ConfigBlob, err = s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		return err
	}
	if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
		return err
	}
	backup := u.ExternalPath + fmt.Sprintf(".fluxlite-backup-%d", u.ID)
	if _, err := sshx.RunCheck(ctx, client.Client, "cp "+shellQuote(u.ExternalPath)+" "+shellQuote(backup)+" && systemctl stop sing-box"); err != nil {
		_ = s.store.UpdateSingBoxUser(ctx, &old)
		return fmt.Errorf("prepare shared sing-box handover: %w", err)
	}
	if err := s.deploySingBoxUser(ctx, node, u, bundle); err != nil {
		_, _ = sshx.Run(ctx, client.Client, "systemctl start sing-box")
		_ = s.store.UpdateSingBoxUser(ctx, &old)
		return err
	}
	if _, err := sshx.RunCheck(ctx, client.Client, "mv "+shellQuote(u.ExternalPath)+" "+shellQuote(u.ExternalPath+".disabled")+" && systemctl start sing-box"); err != nil {
		_, _ = sshx.Run(ctx, client.Client, "systemctl stop "+shellQuote(u.ServiceName)+".service; mv "+shellQuote(u.ExternalPath+".disabled")+" "+shellQuote(u.ExternalPath)+"; systemctl start sing-box")
		_ = s.store.UpdateSingBoxUser(ctx, &old)
		return fmt.Errorf("finish shared sing-box handover: %w", err)
	}
	u.Status = model.SingBoxStatusRunning
	return s.store.UpdateSingBoxUser(ctx, u)
}

func (s *Service) SetSingBoxEnabled(ctx context.Context, id int64, enabled bool) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	if enabled && !u.ExpiresAt.IsZero() && !time.Now().UTC().Before(u.ExpiresAt) {
		return ErrSingBoxExpired
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return err
	}
	if enabled {
		if u.QuotaPausedAt != nil && u.QuotaBytes() > 0 && u.RemainingBytes() == 0 {
			return ErrSingBoxQuotaExhausted
		}
		if err := s.remoteUnit(ctx, node, u, "start"); err != nil {
			return err
		}
		u.Enabled = true
		u.QuotaPausedAt = nil
		u.Status = model.SingBoxStatusRunning
	} else {
		if err := s.remoteUnit(ctx, node, u, "stop"); err != nil {
			return err
		}
		u.Enabled = false
		u.Status = model.SingBoxStatusStopped
	}
	return s.store.UpdateSingBoxUser(ctx, u)
}

func (s *Service) DeleteSingBoxUser(ctx context.Context, id int64) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source == model.SingBoxManaged {
		node, err := s.store.NodeByID(ctx, u.NodeID)
		if err != nil {
			return err
		}
		if err := s.removeSingBoxUser(ctx, node, u); err != nil {
			return err
		}
	}
	return s.store.DeleteSingBoxUser(ctx, id)
}

func (s *Service) AddSingBoxTopUp(ctx context.Context, id, bytes int64) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	if err := s.store.AddSingBoxTopUp(ctx, id, bytes); err != nil {
		return err
	}
	updated, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if updated.QuotaPausedAt != nil && (updated.QuotaBytes() == 0 || updated.RemainingBytes() > 0) {
		return s.SetSingBoxEnabled(ctx, id, true)
	}
	return nil
}

func (s *Service) ResetSingBoxUsage(ctx context.Context, id int64) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	if err := s.store.ResetSingBoxUsage(ctx, id); err != nil {
		return err
	}
	return s.SetSingBoxEnabled(ctx, id, true)
}

func (s *Service) ExportSingBoxBundle(ctx context.Context, id int64, host string) (map[string]any, error) {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, err := s.sealer.Open(u.ConfigBlob)
	if err != nil {
		return nil, err
	}
	var bundle singbox.Bundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return nil, err
	}
	return map[string]any{"protocol": bundle.Protocol, "name": bundle.Name, "port": bundle.Port,
		"config": json.RawMessage(bundle.Config), "link": bundle.Link(host)}, nil
}

func (s *Service) CollectSingBoxTraffic(ctx context.Context) error {
	users, err := s.store.ListSingBoxUsers(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, u := range users {
		if !u.ExpiresAt.IsZero() && !now.Before(u.ExpiresAt) {
			if u.Source == model.SingBoxManaged && u.Enabled {
				node, err := s.store.NodeByID(ctx, u.NodeID)
				if err != nil {
					return err
				}
				if err := s.remoteUnit(ctx, node, u, "stop"); err != nil {
					return err
				}
			}
			if u.Enabled || u.Status != model.SingBoxStatusExpired {
				u.Enabled, u.Status = false, model.SingBoxStatusExpired
				if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
					return err
				}
			}
			continue
		}
		if !now.Before(u.PeriodEndsAt) {
			wasQuotaPaused := u.QuotaPausedAt != nil
			if err := s.store.RollSingBoxPeriod(ctx, u, now); err != nil {
				return err
			}
			if wasQuotaPaused && u.Source == model.SingBoxManaged {
				node, err := s.store.NodeByID(ctx, u.NodeID)
				if err != nil {
					return err
				}
				if err := s.remoteUnit(ctx, node, u, "start"); err != nil {
					return err
				}
				u.Enabled, u.Status = true, model.SingBoxStatusRunning
				if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
					return err
				}
			}
		}
		if u.Source != model.SingBoxManaged {
			continue
		}
		node, err := s.store.NodeByID(ctx, u.NodeID)
		if err != nil {
			return err
		}
		if u.Enabled {
			active, statusErr := s.singBoxUnitActive(ctx, node, u)
			if statusErr == nil && !active && u.QuotaPausedAt == nil {
				u.Status = model.SingBoxStatusStopped
				if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
					return err
				}
				continue
			}
			if statusErr == nil && active && u.Status != model.SingBoxStatusRunning {
				u.Status = model.SingBoxStatusRunning
				if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
					return err
				}
			}
		}
		counters, err := s.readSingBoxCounters(ctx, node, u.ID)
		if err != nil {
			s.log.Debug("sing-box traffic sample failed", "user", u.Name, "error", err)
			continue
		}
		updated, err := s.store.RecordSingBoxTraffic(ctx, u.ID, counters[0], counters[1])
		if err != nil {
			return err
		}
		if updated.QuotaPausedAt != nil && updated.Enabled {
			if err := s.remoteUnit(ctx, node, updated, "stop"); err != nil {
				return err
			}
			updated.Enabled = false
			updated.Status = model.SingBoxStatusExhausted
			if err := s.store.UpdateSingBoxUser(ctx, updated); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) singBoxUnitActive(ctx context.Context, node *model.Node, u *model.SingBoxUser) (bool, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return false, err
	}
	cmd := "systemctl is-active " + shellQuote(u.ServiceName) + ".service 2>/dev/null || true"
	if node.InitSystem == model.InitOpenRC {
		cmd = "rc-service " + shellQuote(u.ServiceName) + " status >/dev/null 2>&1 && echo active || echo inactive"
	}
	res, err := sshx.Run(ctx, client.Client, cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "active", nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (s *Service) deploySingBoxUser(ctx context.Context, node *model.Node, u *model.SingBoxUser, bundle *singbox.Bundle) error {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", node.Name, err)
	}
	bin, err := sshx.RunCheck(ctx, client.Client, "command -v sing-box || command -v /usr/local/bin/sing-box")
	if err != nil {
		return fmt.Errorf("sing-box is not installed on %s; install it first: %w", node.Name, err)
	}
	bin = strings.TrimSpace(strings.Split(bin, "\n")[0])
	dir := fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)
	if _, err := sshx.RunCheck(ctx, client.Client, "mkdir -p "+shellQuote(dir)+" /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)); err != nil {
		return err
	}
	if err := sshx.WriteFile(ctx, client.Client, dir+"/config.json", bundle.Config, "0600"); err != nil {
		return err
	}
	certCmd := fmt.Sprintf("if [ ! -s %s/server.crt ] || [ ! -s %s/server.key ]; then openssl req -x509 -newkey rsa:2048 -nodes -days 825 -subj '/CN=fluxlite' -keyout %s/server.key -out %s/server.crt >/dev/null 2>&1 && chmod 600 %s/server.key; fi", shellQuote(dir), shellQuote(dir), shellQuote(dir), shellQuote(dir), shellQuote(dir))
	if _, err := sshx.RunCheck(ctx, client.Client, certCmd); err != nil {
		return fmt.Errorf("create self-signed certificate: %w", err)
	}
	unit := fmt.Sprintf("[Unit]\nDescription=fluxlite sing-box user %d\nAfter=network-online.target\n[Service]\nExecStart=%s -D /var/lib/fluxlite-singbox/%d -c %s run\nRestart=on-failure\nRestartSec=2\n[Install]\nWantedBy=multi-user.target\n", u.ID, bin, u.ID, dir+"/config.json")
	unitPath := "/etc/systemd/system/" + u.ServiceName + ".service"
	if node.InitSystem == model.InitSystemd {
		if err := sshx.WriteFile(ctx, client.Client, unitPath, []byte(unit), "0644"); err != nil {
			return err
		}
		if _, err := sshx.RunCheck(ctx, client.Client, "systemctl daemon-reload && "+bin+" check -D /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)+" -c "+shellQuote(dir+"/config.json")+" && systemctl enable --now "+shellQuote(u.ServiceName)+".service"); err != nil {
			return fmt.Errorf("start sing-box service: %w", err)
		}
	} else if node.InitSystem == model.InitOpenRC {
		script := fmt.Sprintf("#!/sbin/openrc-run\nname=%q\ndescription=%q\nsupervisor=supervise-daemon\ncommand=%q\ncommand_args=%q\ncommand_background=true\npidfile=%q\nrespawn_delay=2\nrespawn_max=0\ndepend() { need net; after firewall; }\n", u.ServiceName, fmt.Sprintf("fluxlite sing-box user %d", u.ID), bin, fmt.Sprintf("-D /var/lib/fluxlite-singbox/%d -c %s run", u.ID, dir+"/config.json"), "/run/"+u.ServiceName+".pid")
		path := "/etc/init.d/" + u.ServiceName
		if err := sshx.WriteFile(ctx, client.Client, path, []byte(script), "0755"); err != nil {
			return err
		}
		if _, err := sshx.RunCheck(ctx, client.Client, bin+" check -D /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)+" -c "+shellQuote(dir+"/config.json")+" && rc-update add "+shellQuote(u.ServiceName)+" default && rc-service "+shellQuote(u.ServiceName)+" start"); err != nil {
			return fmt.Errorf("start sing-box OpenRC service: %w", err)
		}
	} else {
		return fmt.Errorf("unsupported init system %q on %s", node.InitSystem, node.Name)
	}
	return s.ensureSingBoxAccounting(ctx, client.Client, u.ID, u.Port)
}

func (s *Service) remoteUnit(ctx context.Context, node *model.Node, u *model.SingBoxUser, action string) error {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	cmd := ""
	if node.InitSystem == model.InitSystemd {
		cmd = "systemctl " + action + " " + shellQuote(u.ServiceName) + ".service"
	}
	if node.InitSystem == model.InitOpenRC {
		cmd = "rc-service " + shellQuote(u.ServiceName) + " " + action
	}
	if cmd == "" {
		return fmt.Errorf("unsupported init system %q on %s", node.InitSystem, node.Name)
	}
	_, err = sshx.RunCheck(ctx, client.Client, cmd)
	return err
}

func (s *Service) removeSingBoxUser(ctx context.Context, node *model.Node, u *model.SingBoxUser) error {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	serviceStop := "systemctl disable --now " + shellQuote(u.ServiceName) + ".service 2>/dev/null || true"
	if node.InitSystem == model.InitOpenRC {
		serviceStop = "rc-service " + shellQuote(u.ServiceName) + " stop 2>/dev/null || true; rc-update del " + shellQuote(u.ServiceName) + " default 2>/dev/null || true"
	}
	cmd := serviceStop + "; rm -rf " + shellQuote("/etc/fluxlite/singbox/"+strconv.FormatInt(u.ID, 10)) + " /var/lib/fluxlite-singbox/" + strconv.FormatInt(u.ID, 10) + "; for bin in iptables ip6tables; do if command -v $bin >/dev/null 2>&1; then $bin -S FLUXLITE_SB_ACCT 2>/dev/null | grep 'fluxlite-sb:" + strconv.FormatInt(u.ID, 10) + ":' | sed 's/^-A /-D /' | while read -r rule; do eval \"$bin $rule\" >/dev/null 2>&1 || true; done; fi; done"
	if _, err := sshx.Run(ctx, client.Client, cmd); err != nil {
		return err
	}
	return nil
}

func (s *Service) ensureSingBoxAccounting(ctx context.Context, client *ssh.Client, id int64, port int) error {
	for _, bin := range []string{"iptables", "ip6tables"} {
		cmd := fmt.Sprintf("if command -v %s >/dev/null 2>&1; then %s -N FLUXLITE_SB_ACCT 2>/dev/null || true; %s -C INPUT -j FLUXLITE_SB_ACCT 2>/dev/null || %s -I INPUT 1 -j FLUXLITE_SB_ACCT; %s -C OUTPUT -j FLUXLITE_SB_ACCT 2>/dev/null || %s -I OUTPUT 1 -j FLUXLITE_SB_ACCT; for p in tcp udp; do %s -C FLUXLITE_SB_ACCT -p $p --dport %d -m comment --comment 'fluxlite-sb:%d:in' 2>/dev/null || %s -A FLUXLITE_SB_ACCT -p $p --dport %d -m comment --comment 'fluxlite-sb:%d:in'; %s -C FLUXLITE_SB_ACCT -p $p --sport %d -m comment --comment 'fluxlite-sb:%d:out' 2>/dev/null || %s -A FLUXLITE_SB_ACCT -p $p --sport %d -m comment --comment 'fluxlite-sb:%d:out'; done; fi", bin, bin, bin, bin, bin, bin, bin, port, id, bin, port, id, bin, port, id, bin, port, id)
		if _, err := sshx.Run(ctx, client, cmd); err != nil {
			return err
		}
	}
	return nil
}

var singBoxCounterLine = regexp.MustCompile(`\s+\d+\s+(\d+).*fluxlite-sb:(\d+):(in|out)`)

func (s *Service) readSingBoxCounters(ctx context.Context, node *model.Node, id int64) ([2]int64, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return [2]int64{}, err
	}
	var out [2]int64
	for _, bin := range []string{"iptables", "ip6tables"} {
		res, err := sshx.Run(ctx, client.Client, bin+" -nvxL FLUXLITE_SB_ACCT 2>/dev/null || true")
		if err != nil {
			return out, err
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			m := singBoxCounterLine.FindStringSubmatch(line)
			if len(m) != 4 || m[2] != strconv.FormatInt(id, 10) {
				continue
			}
			bytes, _ := strconv.ParseInt(m[1], 10, 64)
			if m[3] == "in" {
				out[0] += bytes
			} else {
				out[1] += bytes
			}
		}
	}
	return out, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
