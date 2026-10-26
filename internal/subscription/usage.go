package subscription

import (
	"fmt"
	"strings"
	"time"
)

// Usage is what a client shows on its card for the subscription: traffic used
// against the allowance and when the link stops working. A zero Total means
// unlimited and a nil Expire means permanent.
type Usage struct {
	Upload, Download, Total int64
	Expire                  *time.Time
}

// Header renders Usage as the subscription-userinfo value that Clash, Mihomo,
// Shadowrocket, Loon, Quantumult X and v2rayN read. Unlimited and permanent are
// left out rather than written as 0, which clients show as an empty allowance
// and as already expired.
func (u Usage) Header() string {
	parts := []string{fmt.Sprintf("upload=%d", u.Upload), fmt.Sprintf("download=%d", u.Download)}
	if u.Total > 0 {
		parts = append(parts, fmt.Sprintf("total=%d", u.Total))
	}
	if u.Expire != nil {
		parts = append(parts, fmt.Sprintf("expire=%d", u.Expire.Unix()))
	}
	return strings.Join(parts, "; ")
}
