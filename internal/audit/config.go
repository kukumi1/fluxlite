package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// InboundDef is one listener a sing-box config declares.
type InboundDef struct {
	Tag  string
	Type string
	Port int
}

// LogSettings is the effective log section of a sing-box config.
type LogSettings struct {
	Disabled  bool
	Level     string
	Output    string
	Timestamp *bool
}

// Verbose reports whether the level prints the per-connection lines the audit
// is built from. An empty level is sing-box's default, which prints them.
func (l LogSettings) Verbose() bool {
	if l.Disabled {
		return false
	}
	switch strings.ToLower(l.Level) {
	case "", "trace", "debug", "info":
		return true
	}
	return false
}

// ConfigFile is one file a sing-box process loads. FromDir marks a file found
// in a -C directory; sing-box loads those after every -c file.
type ConfigFile struct {
	Path    string
	FromDir bool
	Data    []byte
}

// Config is what the audit needs from a process's merged configuration.
type Config struct {
	Log      LogSettings
	Inbounds []InboundDef
}

type configDoc struct {
	Log *struct {
		Disabled  *bool   `json:"disabled"`
		Level     *string `json:"level"`
		Output    *string `json:"output"`
		Timestamp *bool   `json:"timestamp"`
	} `json:"log"`
	Inbounds []struct {
		Tag        string `json:"tag"`
		Type       string `json:"type"`
		ListenPort int    `json:"listen_port"`
	} `json:"inbounds"`
}

// MergeConfigs merges files the way sing-box does for the fields the audit
// reads: inbounds accumulate, and a later log field overrides an earlier one.
func MergeConfigs(files []ConfigFile) (Config, error) {
	ordered := slices.Clone(files)
	slices.SortStableFunc(ordered, func(a, b ConfigFile) int {
		switch {
		case a.FromDir == b.FromDir:
			return 0
		case a.FromDir:
			return 1
		}
		return -1
	})

	var cfg Config
	for _, f := range ordered {
		if len(bytes.TrimSpace(f.Data)) == 0 {
			continue
		}
		var doc configDoc
		if err := json.Unmarshal(f.Data, &doc); err != nil {
			return Config{}, fmt.Errorf("%s: %w", f.Path, err)
		}
		if l := doc.Log; l != nil {
			if l.Disabled != nil {
				cfg.Log.Disabled = *l.Disabled
			}
			if l.Level != nil {
				cfg.Log.Level = *l.Level
			}
			if l.Output != nil {
				cfg.Log.Output = *l.Output
			}
			if l.Timestamp != nil {
				cfg.Log.Timestamp = l.Timestamp
			}
		}
		for _, in := range doc.Inbounds {
			cfg.Inbounds = append(cfg.Inbounds, InboundDef{Tag: in.Tag, Type: in.Type, Port: in.ListenPort})
		}
	}
	return cfg, nil
}
