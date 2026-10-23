package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// AuditLogPath is where a managed user's sing-box writes its connection log.
// The data directory is already owned by the user's service account, so the
// unprivileged process can create the file there.
func AuditLogPath(id int64) string {
	return fmt.Sprintf("/var/lib/fluxlite-singbox/%d/access.log", id)
}

// auditLog is the log section the panel's traffic audit reads. warn level
// prints nothing per connection; info prints the client address and the
// requested destination under one connection id. A file rather than journald
// so that systemd and OpenRC nodes are read the same way.
func auditLog(id int64) map[string]any {
	return map[string]any{"level": "info", "timestamp": true, "output": AuditLogPath(id)}
}

// WithAuditLog returns config with its log section replaced by the audit one,
// and whether that changed anything. Everything else in the document — an
// adopted config may carry fields this package never generates — is kept.
func WithAuditLog(config json.RawMessage, id int64) (json.RawMessage, bool, error) {
	var doc map[string]any
	if err := json.Unmarshal(config, &doc); err != nil {
		return nil, false, fmt.Errorf("parse sing-box config: %w", err)
	}
	want := auditLog(id)
	if current, ok := doc["log"].(map[string]any); ok && reflect.DeepEqual(normalise(current), normalise(want)) {
		return config, false, nil
	}
	doc["log"] = want
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal sing-box config: %w", err)
	}
	return out, true, nil
}

// normalise round-trips through JSON so a decoded document and a literal map
// compare equal regardless of how their values were typed.
func normalise(v map[string]any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// WithInfoLog returns an operator's own config with its log level raised to
// info and nothing else changed — the order of its top-level fields included,
// so the file still reads the way they wrote it.
func WithInfoLog(config []byte) ([]byte, error) {
	type field struct {
		key   string
		value json.RawMessage
	}
	dec := json.NewDecoder(bytes.NewReader(config))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("sing-box config is not a JSON object")
	}
	var fields []field
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse sing-box config: %w", err)
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("parse sing-box config: %w", err)
		}
		fields = append(fields, field{key, value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("parse sing-box config: %w", err)
	}

	section := map[string]any{}
	at := -1
	for i, f := range fields {
		if f.key == "log" {
			at = i
			if err := json.Unmarshal(f.value, &section); err != nil {
				return nil, fmt.Errorf("parse sing-box log section: %w", err)
			}
		}
	}
	section["level"] = "info"
	delete(section, "disabled")
	raw, err := json.Marshal(section)
	if err != nil {
		return nil, err
	}
	if at < 0 {
		fields = append([]field{{"log", raw}}, fields...)
	} else {
		fields[at].value = raw
	}

	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			compact.WriteByte(',')
		}
		key, _ := json.Marshal(f.key)
		compact.Write(key)
		compact.WriteByte(':')
		compact.Write(f.value)
	}
	compact.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
