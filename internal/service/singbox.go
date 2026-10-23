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
	// Internal ownership is reserved durably before any remote deployment.
	subscriptionID    int64
	NodeID            int64                 `json:"node_id"`
	Name              string                `json:"name"`
	Protocol          model.SingBoxProtocol `json:"protocol"`
	Port              int                   `json:"port"`
	QuotaBytes        int64                 `json:"quota_bytes"`
	Cipher            string                `json:"cipher"`
	AccessDays        int                   `json:"access_days"`
	ExpiresAt         time.Time             `json:"expires_at"`
	UploadLimitMbps   int64                 `json:"upload_limit_mbps"`
	DownloadLimitMbps int64                 `json:"download_limit_mbps"`
}

type SingBoxUserUpdate struct {
	Name              string    `json:"name"`
	QuotaBytes        int64     `json:"quota_bytes"`
	PeriodDays        int       `json:"period_days"`
	ExpiresAt         time.Time `json:"expires_at"`
	Enabled           bool      `json:"enabled"`
	UploadLimitMbps   int64     `json:"upload_limit_mbps"`
	DownloadLimitMbps int64     `json:"download_limit_mbps"`
}

func (s *Service) UpdateSingBoxUser(ctx context.Context, id int64, in SingBoxUserUpdate) error {
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	if strings.TrimSpace(in.Name) == "" {
		return model.ErrSingBoxName
	}
	if in.QuotaBytes < 0 {
		return model.ErrSingBoxQuota
	}
	if in.UploadLimitMbps < 0 || in.DownloadLimitMbps < 0 || in.UploadLimitMbps > 100000 || in.DownloadLimitMbps > 100000 {
		return model.ErrSingBoxRate
	}
	in.ExpiresAt = in.ExpiresAt.UTC()
	if !in.ExpiresAt.IsZero() && !in.ExpiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("connection expiry must be in the future")
	}
	if in.QuotaBytes > 0 && in.QuotaBytes < u.UsedBytes() {
		return model.ErrSingBoxQuota
	}
	u.Name, u.BaseQuotaBytes, u.TopUpBytes, u.ExpiresAt = strings.TrimSpace(in.Name), in.QuotaBytes, 0, in.ExpiresAt
	u.UploadLimitBps, u.DownloadLimitBps = rateBps(in.UploadLimitMbps), rateBps(in.DownloadLimitMbps)
	if in.PeriodDays < 1 || in.PeriodDays > 3650 {
		return model.ErrSingBoxPeriod
	}
	u.PeriodDays = in.PeriodDays
	u.PeriodEndsAt = u.PeriodStartedAt.Add(time.Duration(u.PeriodDays) * 24 * time.Hour)
	if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
		return err
	}
	if node, nodeErr := s.store.NodeByID(ctx, u.NodeID); nodeErr == nil {
		if client, clientErr := s.pool.Get(ctx, node); clientErr == nil {
			u.RateLimitError = ""
			if rateErr := ensureSingBoxLinuxUser(ctx, client.Client, u.ID); rateErr != nil {
				u.RateLimitError = rateErr.Error()
			} else if rateErr := ensureSingBoxServiceIdentity(ctx, client.Client, node, u); rateErr != nil {
				u.RateLimitError = rateErr.Error()
			} else if rateErr := applySingBoxRateLimit(ctx, client.Client, u); rateErr != nil {
				u.RateLimitError = rateErr.Error()
			}
			if rateErr := s.store.UpdateSingBoxUser(ctx, u); rateErr != nil {
				return rateErr
			}
		}
	}
	if u.Enabled != in.Enabled {
		return s.SetSingBoxEnabled(ctx, id, in.Enabled)
	}
	return nil
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
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
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
	if u.Status == model.SingBoxStatusError {
		return errors.New("incomplete deployment must be deleted before recreation")
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
		u.Enabled, u.Status = true, model.SingBoxStatusStopped
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
					Status: model.SingBoxStatusUnknown, ServiceName: "sing-box", ConfigPath: path, PeriodDays: 30,
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
	return s.createSingBoxUser(ctx, in, singBoxCreateOps{
		ports: func(ctx context.Context, node *model.Node) (map[int]bool, error) {
			return s.newLiveLookup(nil).UsedPortsOnNode(ctx, node.ID, nil)
		},
		deploy: s.deploySingBoxUser, cleanup: s.removeSingBoxUser,
	})
}

func (s *Service) createSingBoxUser(ctx context.Context, in SingBoxUserInput, ops singBoxCreateOps) (*model.SingBoxUser, error) {
	// Serialize allocation and reservation with other creates and adoptions.
	release, err := s.lockSingBoxAdoption(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
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
	if !expiresAt.IsZero() && !expiresAt.After(time.Now().UTC()) {
		return nil, fmt.Errorf("connection expiry must be in the future")
	}
	used, err := ops.ports(ctx, node)
	if err != nil {
		return nil, err
	}
	port, err := selectSingBoxPort(ctx, node, in.Port, used, func(ctx context.Context, port int) (bool, error) { return s.store.PortClaimed(ctx, node.ID, port, 0) })
	if err != nil {
		return nil, err
	}

	periodStart := time.Now().UTC()
	u := &model.SingBoxUser{
		NodeID: node.ID, Name: strings.TrimSpace(in.Name), Protocol: in.Protocol,
		Port: port, Enabled: false, Source: model.SingBoxManaged,
		Status: model.SingBoxStatusError, BaseQuotaBytes: in.QuotaBytes, PeriodDays: 30,
		PeriodStartedAt: periodStart, PeriodEndsAt: periodStart.Add(30 * 24 * time.Hour), ExpiresAt: expiresAt,
		ServiceName: "pending", ConfigPath: "pending",
		UploadLimitBps: rateBps(in.UploadLimitMbps), DownloadLimitBps: rateBps(in.DownloadLimitMbps),
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
	// The ID now belongs to this operation. A failed INSERT must never
	// trigger cleanup of another user's existing reservation.
	remoteTouched := false
	rollback := func(cause error) (*model.SingBoxUser, error) {
		removed := false
		err := rollbackSingBoxCreation(ctx, cause, remoteTouched,
			func(ctx context.Context) error { return ops.cleanup(ctx, node, u) },
			func(ctx context.Context) error {
				var err error
				if in.subscriptionID > 0 {
					err = s.store.DeleteSubscriptionMemberAndUser(ctx, u.ID)
				} else {
					err = s.store.DeleteSingBoxUser(ctx, u.ID)
				}
				removed = err == nil
				return err
			},
			func(ctx context.Context) error {
				u.Enabled, u.Status = false, model.SingBoxStatusError
				return s.store.UpdateSingBoxUser(ctx, u)
			})
		if !removed {
			return u, err
		}
		return nil, err
	}
	u.ServiceName = fmt.Sprintf("fluxlite-singbox-%d", u.ID)
	u.ConfigPath = fmt.Sprintf("/etc/fluxlite/singbox/%d/config.json", u.ID)
	bundle, err = singbox.Generate(u.Protocol, u.Name, u.Port, fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID), in.Cipher)
	if err != nil {
		return rollback(err)
	}
	previousConfig := u.ConfigBlob
	u.ConfigBlob, err = s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		return rollback(err)
	}
	if err := s.store.CompareAndSwapSingBoxConfig(ctx, u.ID, previousConfig, u.ConfigBlob); err != nil {
		return rollback(err)
	}
	if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
		return rollback(err)
	}
	if in.subscriptionID > 0 {
		if err := s.store.AddSubscriptionMember(ctx, in.subscriptionID, store.SubscriptionMember{UserID: u.ID, NodeID: u.NodeID, Protocol: string(u.Protocol)}); err != nil {
			return rollback(err)
		}
	}
	remoteTouched = true // Even a failed SSH write may have reached the node.
	if err := ops.deploy(ctx, node, u, bundle); err != nil {
		return rollback(err)
	}
	u.Enabled, u.Status = true, model.SingBoxStatusStopped
	if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
		return rollback(err)
	}

	return u, nil
}

// AdoptSingBoxUser migrates one discovered fragment out of the shared
// installer service into the per-user namespace. The shared service is stopped
// only if it was running; failures restore its fragment and prior running state.
func (s *Service) AdoptSingBoxUser(ctx context.Context, id int64, quota int64) error {
	release, err := s.lockSingBoxAdoption(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Read the source only after acquiring the gate: a queued request must not
	// roll back the independently managed service a prior request just created.
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
	bundle := &singbox.Bundle{Config: json.RawMessage(res.Stdout), Protocol: u.Protocol, Name: u.Name, Port: u.Port}
	now := time.Now().UTC()
	u.Source = model.SingBoxManaged
	u.Enabled = true
	u.Status = model.SingBoxStatusUnknown
	u.ServiceName = fmt.Sprintf("fluxlite-singbox-%d", u.ID)
	u.ConfigPath = fmt.Sprintf("/etc/fluxlite/singbox/%d/config.json", u.ID)
	u.BaseQuotaBytes = quota
	u.TopUpBytes, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut = 0, 0, 0, 0, 0
	u.PeriodStartedAt, u.PeriodEndsAt = now, now.Add(time.Duration(u.PeriodDays)*24*time.Hour)
	u.ExpiresAt = now.Add(365 * 24 * time.Hour)
	u.ConfigBlob, err = s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		return err
	}
	backup := u.ExternalPath + fmt.Sprintf(".fluxlite-backup-%d", u.ID)
	return handoverSingBox(ctx, node.InitSystem, u.ExternalPath, backup,
		func(ctx context.Context, cmd string) (string, error) {
			return sshx.RunCheck(ctx, client.Client, cmd)
		},
		func(ctx context.Context) error { return s.deploySingBoxUser(ctx, node, u, bundle) },
		func(ctx context.Context) error { return s.rollbackAdoptedSingBoxUser(ctx, node, u) },
		func(ctx context.Context) error {
			u.Status = model.SingBoxStatusStopped
			return s.store.UpdateSingBoxUserWithConfig(ctx, u)
		})
}

func (s *Service) SetSingBoxEnabled(ctx context.Context, id int64, enabled bool) error {
	ctx, cancel := context.WithTimeout(ctx, subscriptionMutationTimeout)
	defer cancel()
	owner, err := s.store.SubscriptionForUser(ctx, id)
	if err != nil {
		return err
	}
	if owner != nil && owner.Mode != "collection" {
		release, err := s.lockSubscription(ctx, owner.ID)
		if err != nil {
			return err
		}
		defer release()
	}
	return s.setSingBoxEnabled(ctx, id, enabled)
}
func (s *Service) setSingBoxEnabled(ctx context.Context, id int64, enabled bool) error {
	if enabled {
		if err := s.subscriptionStartAllowed(ctx, id); err != nil {
			return err
		}
	}

	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if enabled && u.Status == model.SingBoxStatusError {
		return errors.New("incomplete deployment must be deleted before recreation")
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
		u.Status = model.SingBoxStatusStopped
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
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
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
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
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

func (s *Service) ReduceSingBoxQuota(ctx context.Context, id, bytes int64) error {
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged {
		return ErrSingBoxNotManaged
	}
	if err := s.store.ReduceSingBoxQuota(ctx, id, bytes); err != nil {
		return err
	}
	updated, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if updated.QuotaPausedAt != nil && updated.Enabled {
		node, err := s.store.NodeByID(ctx, updated.NodeID)
		if err != nil {
			return err
		}
		if err := s.remoteUnit(ctx, node, updated, "stop"); err != nil {
			return err
		}
		updated.Enabled, updated.Status = false, model.SingBoxStatusExhausted
		return s.store.UpdateSingBoxUser(ctx, updated)
	}
	return nil
}

func (s *Service) ResetSingBoxUsage(ctx context.Context, id int64) error {
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return err
	}
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
	if err := s.subscriptionStartAllowed(ctx, id); err != nil {
		return nil, err
	}

	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.Source != model.SingBoxManaged {
		return nil, ErrSingBoxNotManaged
	}
	plain, err := s.sealer.Open(u.ConfigBlob)
	if err != nil {
		return nil, err
	}
	var bundle singbox.Bundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return nil, err
	}
	// Older adopted bundles contain only the original server JSON and no link
	// template. Derive client credentials on demand without rewriting the DB,
	// redeploying the service, or changing existing client passwords.
	bundle.Name, bundle.Port = u.Name, u.Port
	link, clientConfig, err := bundle.ExportClient(host)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"protocol": bundle.Protocol, "name": u.Name, "port": u.Port,
		"config": clientConfig, "link": link}
	var client struct {
		Outbounds []struct {
			Type string `json:"type"`
			TLS  struct {
				Insecure bool `json:"insecure"`
				Reality  struct {
					Enabled bool `json:"enabled"`
				} `json:"reality"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(clientConfig, &client); err != nil {
		return nil, fmt.Errorf("decode client export: %w", err)
	}
	for _, outbound := range client.Outbounds {
		if outbound.Type == "vless" {
			result["protocol"] = model.SingBoxVLESSTLS
			if outbound.TLS.Reality.Enabled {
				result["protocol"] = model.SingBoxVLESSReality
			}
		}
		if outbound.TLS.Insecure {
			result["warnings"] = []string{"此导出配置跳过 TLS 证书校验。连接前请确认目标地址和证书；如使用受信任证书，请在客户端开启证书校验并设置正确的 SNI。"}
			break
		}
	}
	if bundle.LinkTemplate == "" && (bundle.ClientParameters == nil || bundle.ClientParameters.SNI == "") {
		var original struct {
			Inbounds []struct {
				Port int `json:"listen_port"`
				TLS  struct {
					Enabled    bool   `json:"enabled"`
					ServerName string `json:"server_name"`
					Reality    struct {
						Enabled bool `json:"enabled"`
					} `json:"reality"`
				} `json:"tls"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(bundle.Config, &original); err != nil {
			return nil, fmt.Errorf("decode original export: %w", err)
		}
		for _, inbound := range original.Inbounds {
			if inbound.Port == u.Port && inbound.TLS.Enabled && inbound.TLS.ServerName == "" && !inbound.TLS.Reality.Enabled {
				warnings, _ := result["warnings"].([]string)
				result["warnings"] = append(warnings, "服务端配置未提供可恢复的客户端 SNI；已按原始服务端配置导出并保留证书校验，未猜测或写入客户端专属参数。")
				break
			}
		}
	}
	return result, nil
}

type singBoxTrafficOps struct {
	unit     func(context.Context, *model.Node, *model.SingBoxUser, string) error
	counters func(context.Context, *model.Node, int64) ([2]int64, error)
	online   func(context.Context, *model.Node, *model.SingBoxUser) (bool, error)
}

func (s *Service) CollectSingBoxTraffic(ctx context.Context) error {
	return s.collectSingBoxTraffic(ctx, singBoxTrafficOps{unit: s.remoteUnit, counters: s.readSingBoxCounters, online: s.singBoxUserOnline})
}

// Each member gets an independent bounded deadline. An unreachable machine or
// failed stop must not starve another node's quota and expiry enforcement.
func (s *Service) collectSingBoxTraffic(ctx context.Context, ops singBoxTrafficOps) error {
	users, err := s.store.ListSingBoxUsers(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return runSubscriptionMembers(ctx, ids, func(child context.Context, id int64) error {
		u, err := s.store.SingBoxUserByID(child, id)
		if err != nil {
			return err
		}
		return s.collectSingBoxUserTraffic(child, u, time.Now().UTC(), ops)
	})
}

func (s *Service) collectSingBoxUserTraffic(ctx context.Context, u *model.SingBoxUser, now time.Time, ops singBoxTrafficOps) error {
	owner, ownerErr := s.store.SubscriptionForUser(ctx, u.ID)
	if ownerErr != nil {
		return ownerErr
	}
	if owner != nil && owner.Mode != "collection" {
		return nil
	}

	if u.Status == model.SingBoxStatusError {
		return nil
	}

	if !u.ExpiresAt.IsZero() && !now.Before(u.ExpiresAt) {
		if u.Source == model.SingBoxManaged && u.Enabled {
			node, err := s.store.NodeByID(ctx, u.NodeID)
			if err != nil {
				return err
			}
			if err := ops.unit(ctx, node, u, "stop"); err != nil {
				return err
			}
		}
		if u.Enabled || u.Status != model.SingBoxStatusExpired {
			u.Enabled, u.Status = false, model.SingBoxStatusExpired
			if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
				return err
			}
		}
		return nil
	}
	if !now.Before(u.PeriodEndsAt) {
		wasQuotaPaused := u.QuotaPausedAt != nil
		if err := s.store.RollSingBoxPeriod(ctx, u, now); err != nil {
			return err
		}
		if wasQuotaPaused && u.Source == model.SingBoxManaged && s.subscriptionStartAllowed(ctx, u.ID) == nil {
			node, err := s.store.NodeByID(ctx, u.NodeID)
			if err != nil {
				return err
			}
			if err := ops.unit(ctx, node, u, "start"); err != nil {
				return err
			}
			u.Enabled, u.Status = true, model.SingBoxStatusStopped
			if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
				return err
			}
		}
	}
	if u.Source != model.SingBoxManaged {
		return nil
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return err
	}
	counters, err := ops.counters(ctx, node, u.ID)
	if err != nil {
		return fmt.Errorf("sample sing-box user %d: %w", u.ID, err)
	}
	if u.Enabled && u.QuotaPausedAt == nil {
		online, statusErr := ops.online(ctx, node, u)
		if statusErr == nil {
			// A packet seen since the previous counter sample also counts as
			// activity. This covers UDP clients on hosts without conntrack.
			recentActivity := counters[0] > u.RawIn || counters[1] > u.RawOut
			desired := model.SingBoxStatusStopped
			if online || recentActivity {
				desired = model.SingBoxStatusRunning
			}
			if u.Status != desired {
				u.Status = desired
				if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
					return err
				}
			}
		} else {
			s.log.Debug("sing-box online probe failed", "user", u.Name, "error", statusErr)
		}
	}
	if err := s.store.RecordSubscriptionCounters(ctx, u.ID, counters[0], counters[1]); err != nil {
		return err
	}
	updated, err := s.store.RecordSingBoxTraffic(ctx, u.ID, counters[0], counters[1])
	if err != nil {
		return err
	}
	if updated.QuotaPausedAt != nil && updated.Enabled {
		if err := ops.unit(ctx, node, updated, "stop"); err != nil {
			return err
		}
		updated.Enabled = false
		updated.Status = model.SingBoxStatusExhausted
		if err := s.store.UpdateSingBoxUser(ctx, updated); err != nil {
			return err
		}
	}
	return nil
}

// SampleSingBoxUserStatus updates only the presence indicator. Enabled is the
// administrator's availability switch; Status is derived from live sessions
// and therefore becomes offline when the client disconnects without stopping
// the user's service.
func (s *Service) SampleSingBoxUserStatus(ctx context.Context, id int64) error {
	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Source != model.SingBoxManaged || !u.Enabled || u.QuotaPausedAt != nil ||
		(!u.ExpiresAt.IsZero() && !time.Now().UTC().Before(u.ExpiresAt)) {
		return nil
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return err
	}
	online, err := s.singBoxUserOnline(ctx, node, u)
	if err != nil {
		return err
	}
	if counters, counterErr := s.readSingBoxCounters(ctx, node, u.ID); counterErr == nil {
		online = online || counters[0] > u.RawIn || counters[1] > u.RawOut
	}
	if online == (u.Status == model.SingBoxStatusRunning) {
		return nil
	}
	if online {
		u.Status = model.SingBoxStatusRunning
	} else {
		u.Status = model.SingBoxStatusStopped
	}
	return s.store.UpdateSingBoxUser(ctx, u)
}

// singBoxUserOnline reports whether a managed sing-box inbound currently has
// a client session. TCP is read from the kernel socket tables; UDP uses
// conntrack when available. The traffic-counter fallback in
// CollectSingBoxTraffic covers UDP-only hosts without conntrack.
func (s *Service) singBoxUserOnline(ctx context.Context, node *model.Node, u *model.SingBoxUser) (bool, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return false, err
	}
	port := strconv.Itoa(u.Port)
	cmd := "port=" + shellQuote(port) + "\n" +
		"hexport=$(printf '%04X' \"$port\")\n" +
		"tcp=0\n" +
		"for table in /proc/net/tcp /proc/net/tcp6; do\n" +
		"  if [ -r \"$table\" ]; then\n" +
		"    n=$(awk -v p=\"$hexport\" 'NR > 1 { split($2, a, \":\"); if (tolower(a[length(a)]) == tolower(p) && $4 == \"01\") n++ } END { print n + 0 }' \"$table\")\n" +
		"    tcp=$((tcp + n))\n" +
		"  fi\n" +
		"done\n" +
		"udp=0\n" +
		"if command -v conntrack >/dev/null 2>&1; then\n" +
		"  udp=$(conntrack -L -p udp 2>/dev/null | awk -v p=\"$port\" '($0 ~ (\"dport=\" p) || $0 ~ (\"sport=\" p)) && ($0 ~ /ESTABLISHED|UNREPLIED/) { n++ } END { print n + 0 }')\n" +
		"fi\n" +
		"if [ \"$udp\" -eq 0 ]; then\n" +
		"  for table in /proc/net/nf_conntrack /proc/net/ip_conntrack; do\n" +
		"    if [ -r \"$table\" ]; then\n" +
		"      n=$(awk -v p=\"$port\" '$0 ~ /udp/ && ($0 ~ (\"dport=\" p) || $0 ~ (\"sport=\" p)) && ($0 ~ /ESTABLISHED|UNREPLIED/) { n++ } END { print n + 0 }' \"$table\")\n" +
		"      udp=$((udp + n))\n" +
		"    fi\n" +
		"  done\n" +
		"fi\n" +
		"if [ \"$tcp\" -gt 0 ] || [ \"$udp\" -gt 0 ]; then echo ONLINE; else echo OFFLINE; fi"
	res, err := sshx.Run(ctx, client.Client, cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "ONLINE", nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func rateBps(mbps int64) int64 {
	if mbps <= 0 {
		return 0
	}
	return mbps * 1_000_000
}

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
	if err := ensureSingBoxLinuxUser(ctx, client.Client, u.ID); err != nil {
		return fmt.Errorf("create isolated sing-box user: %w", err)
	}
	if _, err := sshx.RunCheck(ctx, client.Client, "mkdir -p "+shellQuote(dir)+" /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)); err != nil {
		return err
	}
	config, _, err := singbox.WithAuditLog(bundle.Config, u.ID)
	if err != nil {
		return err
	}
	if err := sshx.WriteFile(ctx, client.Client, dir+"/config.json", config, "0600"); err != nil {
		return err
	}
	certCmd := fmt.Sprintf("if [ ! -s %s/server.crt ] || [ ! -s %s/server.key ]; then openssl req -x509 -newkey rsa:2048 -nodes -days 825 -subj '/CN=fluxlite' -keyout %s/server.key -out %s/server.crt >/dev/null 2>&1 && chmod 600 %s/server.key; fi", shellQuote(dir), shellQuote(dir), shellQuote(dir), shellQuote(dir), shellQuote(dir))
	if _, err := sshx.RunCheck(ctx, client.Client, certCmd); err != nil {
		return fmt.Errorf("create self-signed certificate: %w", err)
	}
	unit := fmt.Sprintf("[Unit]\nDescription=fluxlite sing-box user %d\nAfter=network-online.target\n[Service]\nUser=%s\nGroup=%s\nExecStart=%s -D /var/lib/fluxlite-singbox/%d -c %s run\nRestart=on-failure\nRestartSec=2\n[Install]\nWantedBy=multi-user.target\n", u.ID, singBoxLinuxUser(u.ID), singBoxLinuxUser(u.ID), bin, u.ID, dir+"/config.json")
	unitPath := "/etc/systemd/system/" + u.ServiceName + ".service"
	if _, err := sshx.RunCheck(ctx, client.Client, "chown -R "+shellQuote(singBoxLinuxUser(u.ID))+":"+shellQuote(singBoxLinuxUser(u.ID))+" "+shellQuote(dir)+" /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)); err != nil {
		return err
	}
	if node.InitSystem == model.InitSystemd {
		if err := sshx.WriteFile(ctx, client.Client, unitPath, []byte(unit), "0644"); err != nil {
			return err
		}
		if _, err := sshx.RunCheck(ctx, client.Client, "systemctl daemon-reload && "+bin+" check -D /var/lib/fluxlite-singbox/"+strconv.FormatInt(u.ID, 10)+" -c "+shellQuote(dir+"/config.json")+" && systemctl enable --now "+shellQuote(u.ServiceName)+".service"); err != nil {
			return fmt.Errorf("start sing-box service: %w", err)
		}
	} else if node.InitSystem == model.InitOpenRC {
		script := fmt.Sprintf("#!/sbin/openrc-run\nname=%q\ndescription=%q\ncommand_user=%q\nsupervisor=supervise-daemon\ncommand=%q\ncommand_args=%q\ncommand_background=true\npidfile=%q\nrespawn_delay=2\nrespawn_max=0\ndepend() { need net; after firewall; }\n", u.ServiceName, fmt.Sprintf("fluxlite sing-box user %d", u.ID), singBoxLinuxUser(u.ID), bin, fmt.Sprintf("-D /var/lib/fluxlite-singbox/%d -c %s run", u.ID, dir+"/config.json"), "/run/"+u.ServiceName+".pid")
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
	if err := s.ensureSingBoxAccounting(ctx, client.Client, u.ID, u.Port); err != nil {
		return err
	}
	if err := applySingBoxRateLimit(ctx, client.Client, u); err != nil {
		u.RateLimitError = err.Error()
	} else {
		u.RateLimitError = ""
	}
	return nil
}

func (s *Service) remoteUnit(ctx context.Context, node *model.Node, u *model.SingBoxUser, action string) error {
	cmd, err := singBoxUnitCommand(node.InitSystem, u, action)
	if err != nil {
		return err
	}
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return &singBoxOperationError{"connect for sing-box availability change failed", err}
	}
	if _, err := sshx.RunCheck(ctx, client.Client, cmd); err != nil {
		return &singBoxOperationError{"sing-box availability change or verification failed", err}
	}
	return nil
}

func (s *Service) removeSingBoxUser(ctx context.Context, node *model.Node, u *model.SingBoxUser) error {
	cmd, err := singBoxRemovalCommand(node.InitSystem, u)
	if err != nil {
		return err
	}
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	_, err = sshx.RunCheck(ctx, client.Client, cmd)
	if err == nil {
		_ = removeSingBoxRateLimit(ctx, client.Client, u.ID)
		_, _ = sshx.Run(ctx, client.Client, "userdel "+shellQuote(singBoxLinuxUser(u.ID))+" 2>/dev/null || true")
	}
	return err
}

func (s *Service) ensureSingBoxAccounting(ctx context.Context, client *ssh.Client, id int64, port int) error {
	for _, bin := range []string{"iptables", "ip6tables"} {
		cmd := fmt.Sprintf("command -v %s >/dev/null 2>&1 || exit 1; %s -N FLUXLITE_SB_ACCT 2>/dev/null || true; %s -C INPUT -j FLUXLITE_SB_ACCT 2>/dev/null || %s -I INPUT 1 -j FLUXLITE_SB_ACCT; %s -C OUTPUT -j FLUXLITE_SB_ACCT 2>/dev/null || %s -I OUTPUT 1 -j FLUXLITE_SB_ACCT; for p in tcp udp; do %s -C FLUXLITE_SB_ACCT -p $p --dport %d -m comment --comment 'fluxlite-sb:%d:in' 2>/dev/null || %s -A FLUXLITE_SB_ACCT -p $p --dport %d -m comment --comment 'fluxlite-sb:%d:in'; %s -C FLUXLITE_SB_ACCT -p $p --sport %d -m comment --comment 'fluxlite-sb:%d:out' 2>/dev/null || %s -A FLUXLITE_SB_ACCT -p $p --sport %d -m comment --comment 'fluxlite-sb:%d:out'; done", bin, bin, bin, bin, bin, bin, bin, port, id, bin, port, id, bin, port, id, bin, port, id)
		if _, err := sshx.RunCheck(ctx, client, "set -e\n"+cmd); err != nil {
			return err
		}
	}
	return nil
}

var singBoxCounterLine = regexp.MustCompile(`\s+\d+\s+(\d+).*fluxlite-sb:(\d+):(in|out)`)

// errSingBoxAccountingMissing means a node's counting rules are gone from the
// kernel, as opposed to unreadable.
var errSingBoxAccountingMissing = errors.New("accounting rules missing")

func (s *Service) readSingBoxCounters(ctx context.Context, node *model.Node, id int64) ([2]int64, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return [2]int64{}, errors.New("accounting connection unavailable")
	}
	sample := func() ([2]int64, error) { return sampleSingBoxCounters(ctx, client.Client, id) }
	repair := func() error {
		u, err := s.store.SingBoxUserByID(ctx, id)
		if err != nil {
			return err
		}
		if err := s.ensureSingBoxAccounting(ctx, client.Client, id, u.Port); err != nil {
			return err
		}
		s.log.Warn("sing-box accounting rules were missing and have been restored", "user", u.Name, "node", node.Name)
		return nil
	}
	return sampleWithRepair(sample, repair)
}

// sampleWithRepair reinstalls missing counting rules and samples again. The
// rules live only in the kernel: a reboot or a firewall reload drops them and
// nothing else puts them back, so without this a node would go on serving
// traffic that is never counted against its quota. Bytes carried while the
// rules were gone are lost; the counters restart from zero, which recording
// already treats as a reset.
func sampleWithRepair(sample func() ([2]int64, error), repair func() error) ([2]int64, error) {
	out, err := sample()
	if !errors.Is(err, errSingBoxAccountingMissing) {
		return out, err
	}
	if rerr := repair(); rerr != nil {
		return out, fmt.Errorf("%w; restoring them failed: %v", err, rerr)
	}
	return sample()
}

func sampleSingBoxCounters(ctx context.Context, client *ssh.Client, id int64) ([2]int64, error) {
	var out [2]int64
	for _, bin := range []string{"iptables", "ip6tables"} {
		res, err := sshx.Run(ctx, client, bin+" -C INPUT -j FLUXLITE_SB_ACCT && "+bin+" -C OUTPUT -j FLUXLITE_SB_ACCT")
		// 127 is the shell saying the binary is not installed: nothing to
		// restore there.
		if err != nil || res.ExitCode == 127 {
			return out, errors.New("accounting chain unavailable")
		}
		if res.ExitCode != 0 {
			return out, errSingBoxAccountingMissing
		}
		res, err = sshx.Run(ctx, client, bin+" -nvxL FLUXLITE_SB_ACCT")
		if err != nil || res.ExitCode != 0 {
			return out, errors.New("accounting chain unavailable")
		}
		v, err := parseSingBoxCounters(res.Stdout, id)
		if err != nil {
			return out, err
		}
		for i := range out {
			if v[i] > (1<<63-1)-out[i] {
				return out, errors.New("accounting counter overflow")
			}
			out[i] += v[i]
		}
	}
	return out, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
