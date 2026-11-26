// Package routeclass names the line a return route takes into China: which
// carrier's network a trace enters first, and which of that carrier's
// backbones (CN2, 163, 9929, CMIN2, ...) carries it.
package routeclass

//go:generate go run ./gen -o prefixes_v4.txt

import (
	_ "embed"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed prefixes_v4.txt
var prefixData string

// unannounced are backbone interconnect ranges the carriers do not announce,
// so they are missing from any BGP table yet appear in nearly every trace.
// They join the table rather than backing it up: 59.43.0.0/16 sits inside
// Guangdong Telecom's 59.32.0.0/11, and only the longer match tells CN2 apart.
var unannounced = []struct {
	prefix string
	asn    int
}{
	{"59.43.0.0/16", 4809},
	{"69.194.160.0/20", 23764},
	{"203.22.176.0/22", 23764},
	{"219.158.0.0/17", 4837},
}

type prefixTable struct {
	byBits [33]map[uint32]int
	bits   []int
}

var (
	tableOnce sync.Once
	table     *prefixTable
)

func loadTable() *prefixTable {
	tableOnce.Do(func() {
		t := &prefixTable{}
		for n, line := range strings.Split(prefixData, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			p, asn, err := parseEntry(line)
			if err != nil {
				panic(fmt.Sprintf("routeclass: prefixes_v4.txt line %d: %v", n+1, err))
			}
			t.add(p, asn)
		}
		for _, u := range unannounced {
			t.add(netip.MustParsePrefix(u.prefix), u.asn)
		}
		table = t
	})
	return table
}

func parseEntry(line string) (netip.Prefix, int, error) {
	cidr, asText, ok := strings.Cut(line, " ")
	if !ok {
		return netip.Prefix{}, 0, fmt.Errorf("want \"<prefix> <asn>\"")
	}
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, 0, fmt.Errorf("bad prefix %q", cidr)
	}
	asn, err := strconv.Atoi(asText)
	if err != nil {
		return netip.Prefix{}, 0, fmt.Errorf("bad asn %q", asText)
	}
	return p.Masked(), asn, nil
}

func (t *prefixTable) add(p netip.Prefix, asn int) {
	b := p.Bits()
	if t.byBits[b] == nil {
		t.byBits[b] = map[uint32]int{}
		t.bits = append(t.bits, b)
		sort.Sort(sort.Reverse(sort.IntSlice(t.bits)))
	}
	key := v4(p.Addr())
	if _, dup := t.byBits[b][key]; !dup {
		t.byBits[b][key] = asn
	}
}

func (t *prefixTable) lookup(addr netip.Addr) int {
	n := v4(addr)
	for _, b := range t.bits {
		if asn, ok := t.byBits[b][n&mask(b)]; ok {
			return asn
		}
	}
	return 0
}

func v4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func mask(bits int) uint32 {
	if bits == 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

// ASNOf returns the origin AS of an IPv4 address among the Chinese carrier
// networks, or 0 when it belongs to none of them.
func ASNOf(ip string) int {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return 0
	}
	return loadTable().lookup(addr)
}

// PrefixDate is when the embedded prefix table was generated.
func PrefixDate() string {
	first, _, _ := strings.Cut(prefixData, "\n")
	if i := strings.LastIndex(first, ", "); i >= 0 {
		return strings.TrimSuffix(strings.TrimSpace(first[i+2:]), ".")
	}
	return ""
}
