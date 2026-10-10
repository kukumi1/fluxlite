package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type SingBoxProtocol string

const (
	SingBoxSS2022       SingBoxProtocol = "ss2022"
	SingBoxAnyTLS       SingBoxProtocol = "anytls"
	SingBoxVLESSReality SingBoxProtocol = "vless-reality"
	SingBoxHysteria2    SingBoxProtocol = "hysteria2"
	SingBoxTUIC         SingBoxProtocol = "tuic"
	SingBoxTrojan       SingBoxProtocol = "trojan"
	SingBoxVMess        SingBoxProtocol = "vmess"
	SingBoxVLESSTLS     SingBoxProtocol = "vless-tls"
)

func (p SingBoxProtocol) Valid() bool {
	switch p {
	case SingBoxSS2022, SingBoxAnyTLS, SingBoxVLESSReality, SingBoxHysteria2,
		SingBoxTUIC, SingBoxTrojan, SingBoxVMess, SingBoxVLESSTLS:
		return true
	default:
		return false
	}
}

type SingBoxSource string

const (
	SingBoxManaged  SingBoxSource = "managed"
	SingBoxExternal SingBoxSource = "external"
)

type SingBoxStatus string

const (
	SingBoxStatusUnknown   SingBoxStatus = "unknown"
	SingBoxStatusRunning   SingBoxStatus = "running"
	SingBoxStatusStopped   SingBoxStatus = "stopped"
	SingBoxStatusExhausted SingBoxStatus = "exhausted"
	SingBoxStatusExpired   SingBoxStatus = "expired"
	SingBoxStatusError     SingBoxStatus = "error"
)

var (
	ErrSingBoxName     = errors.New("sing-box user name must not be empty")
	ErrSingBoxProtocol = errors.New("unsupported sing-box protocol")
	ErrSingBoxPort     = errors.New("sing-box port must be between 1 and 65535")
	ErrSingBoxQuota    = errors.New("sing-box quota must be zero (unlimited) or greater than zero")
	ErrSingBoxPeriod   = errors.New("sing-box period must end after it starts")
	ErrSingBoxNode     = errors.New("sing-box user must belong to a managed node")
)

type SingBoxUser struct {
	ID       int64           `json:"id"`
	NodeID   int64           `json:"node_id"`
	Name     string          `json:"name"`
	Protocol SingBoxProtocol `json:"protocol"`
	Port     int             `json:"port"`
	Enabled  bool            `json:"enabled"`
	Source   SingBoxSource   `json:"source"`
	Status   SingBoxStatus   `json:"status"`

	ServiceName  string `json:"service_name"`
	ConfigPath   string `json:"config_path"`
	ExternalPath string `json:"external_path,omitempty"`
	ConfigBlob   []byte `json:"-"`

	BaseQuotaBytes  int64      `json:"base_quota_bytes"`
	TopUpBytes      int64      `json:"top_up_bytes"`
	UsedIn          int64      `json:"used_in"`
	UsedOut         int64      `json:"used_out"`
	RawIn           int64      `json:"-"`
	RawOut          int64      `json:"-"`
	PeriodStartedAt time.Time  `json:"period_started_at"`
	PeriodEndsAt    time.Time  `json:"period_ends_at"`
	PeriodDays      int        `json:"period_days"`
	ExpiresAt       time.Time  `json:"expires_at"`
	QuotaPausedAt   *time.Time `json:"quota_paused_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (u *SingBoxUser) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return ErrSingBoxName
	}
	if !u.Protocol.Valid() {
		return ErrSingBoxProtocol
	}
	if u.Port < 1 || u.Port > 65535 {
		return ErrSingBoxPort
	}
	if u.NodeID <= 0 {
		return ErrSingBoxNode
	}
	if u.BaseQuotaBytes < 0 {
		return ErrSingBoxQuota
	}
	if !u.PeriodEndsAt.After(u.PeriodStartedAt) {
		return ErrSingBoxPeriod
	}
	if u.PeriodDays < 1 || u.PeriodDays > 3650 {
		return ErrSingBoxPeriod
	}
	return nil
}

func (u *SingBoxUser) QuotaBytes() int64 {
	if u.BaseQuotaBytes == 0 {
		return 0
	}
	if u.TopUpBytes > 0 && u.BaseQuotaBytes > (1<<63-1)-u.TopUpBytes {
		return 1<<63 - 1
	}
	return u.BaseQuotaBytes + u.TopUpBytes
}

func (u *SingBoxUser) UsedBytes() int64 {
	if u.UsedIn > (1<<63-1)-u.UsedOut {
		return 1<<63 - 1
	}
	return u.UsedIn + u.UsedOut
}

func (u *SingBoxUser) RemainingBytes() int64 {
	if u.QuotaBytes() == 0 {
		return 0
	}
	remaining := u.QuotaBytes() - u.UsedBytes()
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (u *SingBoxUser) String() string { return fmt.Sprintf("%s/%s:%d", u.Name, u.Protocol, u.Port) }
