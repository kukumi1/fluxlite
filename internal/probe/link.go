package probe

import (
	"context"
	"encoding/json"
	"net"
	"time"
)

// resolveTimeout bounds looking up one machine's host name.
const resolveTimeout = 2 * time.Second

// Link pairs each probe machine with the panel's machine at the same
// address, so one machine shows once with the agent's live readings and the
// panel's SSH. An address shared by two panel machines (NAT hosts behind
// one IP) says nothing about which is which, and is not used.
func (h *Hub) Link(ctx context.Context) {
	candidates, err := h.store.ProbeLinkCandidates(ctx)
	if err != nil {
		h.log.Error("list probe machines to match", "error", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	nodes, err := h.store.ListNodes(ctx)
	if err != nil {
		h.log.Error("list machines to match probes with", "error", err)
		return
	}
	owners := map[string][]int64{}
	for _, n := range nodes {
		for _, addr := range h.addresses(ctx, n.Host) {
			owners[addr] = append(owners[addr], n.ID)
		}
	}
	linked := 0
	for _, c := range candidates {
		var info struct {
			IPv4 string `json:"ipv4"`
			IPv6 string `json:"ipv6"`
		}
		if json.Unmarshal(c.Info, &info) != nil {
			continue
		}
		for _, addr := range []string{canonicalIP(info.IPv4), canonicalIP(info.IPv6)} {
			if ids := owners[addr]; addr != "" && len(ids) == 1 {
				ok, err := h.store.AutoLinkProbeClient(ctx, c.UUID, ids[0])
				if err != nil {
					h.log.Error("match probe machine", "machine", c.Name, "error", err)
				}
				if ok {
					linked++
				}
				break
			}
		}
	}
	if linked == 0 {
		return
	}
	if err := h.store.SyncProbeWeights(ctx); err != nil {
		h.log.Error("order probe machines", "error", err)
	}
	h.log.Info("matched probe machines to the panel's machines", "count", linked)
}

func (h *Hub) addresses(ctx context.Context, host string) []string {
	if ip := canonicalIP(host); ip != "" {
		return []string{ip}
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	found, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range found {
		if ip := canonicalIP(a); ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

func canonicalIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}
