package routeclass

import (
	"fmt"
	"net/netip"
	"strings"
)

// Hop is one TTL of a trace: the first address that answered, or none.
type Hop struct {
	TTL int      `json:"ttl"`
	IP  string   `json:"ip,omitempty"`
	MS  *float64 `json:"ms,omitempty"`
	ASN int      `json:"asn,omitempty"`
	Net string   `json:"net,omitempty"`
}

// Confidence says how far a verdict can be trusted.
const (
	Confirmed    = "confirmed"
	Inferred     = "inferred"
	Insufficient = "insufficient"
)

// Tiers rank lines the way they are sold: premium, quality, ordinary.
const (
	TierUnknown = iota
	TierOrdinary
	TierQuality
	TierPremium
)

// Result is the verdict on one return route.
type Result struct {
	Line       string   `json:"line"`
	Tier       int      `json:"tier"`
	Confidence string   `json:"confidence"`
	Evidence   string   `json:"evidence"`
	Chain      []string `json:"chain"`
	// Entry is the carrier whose network the route enters first; Handoff the
	// one it is handed to inside China, when that differs.
	Entry   string `json:"entry,omitempty"`
	Handoff string `json:"handoff,omitempty"`
	Reached bool   `json:"reached"`
}

type family string

const (
	telecom family = "电信"
	unicom  family = "联通"
	mobile  family = "移动"
)

var families = map[int]family{
	4134: telecom, 4809: telecom, 23764: telecom, 4811: telecom, 4812: telecom, 4813: telecom,
	4816: telecom, 23650: telecom, 23724: telecom,
	4837: unicom, 9929: unicom, 10099: unicom, 4808: unicom, 9800: unicom, 17621: unicom,
	17622: unicom, 17623: unicom, 17816: unicom, 135061: unicom,
	9808: mobile, 58453: mobile, 58807: mobile, 24400: mobile, 56040: mobile, 56041: mobile,
	56042: mobile, 56044: mobile, 56046: mobile, 56047: mobile, 56048: mobile,
}

// Backbone names. AS4134, AS4837 and AS9808 hold provincial networks as well
// as a backbone; only their interconnect ranges count as the backbone.
const (
	netCN2    = "CN2"
	netCTGNet = "CTGNet"
	net163    = "163"
	net9929   = "9929"
	netCUG    = "CUG"
	net169    = "4837"
	netCMIN2  = "CMIN2"
	netCMI    = "CMI"
	netCMNET  = "CMNET"
)

var backboneRanges = []struct {
	prefix netip.Prefix
	asn    int
	net    string
}{
	{netip.MustParsePrefix("202.97.0.0/16"), 4134, net163},
	{netip.MustParsePrefix("219.158.0.0/16"), 4837, net169},
	{netip.MustParsePrefix("221.183.0.0/16"), 9808, netCMNET},
	{netip.MustParsePrefix("111.24.0.0/14"), 9808, netCMNET},
}

func backboneOf(addr netip.Addr, asn int) string {
	switch asn {
	case 4809:
		return netCN2
	case 23764:
		return netCTGNet
	case 9929:
		return net9929
	case 10099:
		return netCUG
	case 58807:
		return netCMIN2
	case 58453:
		return netCMI
	}
	for _, r := range backboneRanges {
		if r.asn == asn && r.prefix.Contains(addr) {
			return r.net
		}
	}
	return ""
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"),
}

func isPrivate(a netip.Addr) bool {
	for _, p := range privateRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Annotate fills in each hop's origin AS and backbone.
func Annotate(hops []Hop) {
	for i := range hops {
		addr, err := netip.ParseAddr(hops[i].IP)
		if err != nil || !addr.Is4() || isPrivate(addr) {
			continue
		}
		hops[i].ASN = ASNOf(hops[i].IP)
		hops[i].Net = backboneOf(addr, hops[i].ASN)
	}
}

type pubHop struct {
	index int
	asn   int
	net   string
	fam   family
	dst   bool
}

// Classify names the line of an annotated trace towards dst.
//
// Only the stretch inside the first carrier the route enters decides the
// line: a route that comes in over 163 and is then handed to Unicom is a 163
// route, not 4837. Within it, a premium backbone wins over an ordinary one,
// and the destination itself is never evidence of a backbone.
func Classify(hops []Hop, dst string) Result {
	var pub []pubHop
	reached := false
	for i, h := range hops {
		addr, err := netip.ParseAddr(h.IP)
		if err != nil || isPrivate(addr) {
			continue
		}
		isDst := h.IP == dst
		reached = reached || isDst
		pub = append(pub, pubHop{index: i, asn: h.ASN, net: h.Net, fam: families[h.ASN], dst: isDst})
	}
	res := Result{Chain: []string{}, Reached: reached}

	entry := -1
	for i, h := range pub {
		if h.fam != "" {
			entry = i
			break
		}
	}
	if entry < 0 {
		res.Line, res.Confidence = "未识别", Insufficient
		if len(pub) == 0 {
			res.Evidence = "没有任何公网跳点回应"
		} else {
			res.Evidence = "路径中没有出现三网的地址"
		}
		return res
	}

	fam := pub[entry].fam
	res.Entry = string(fam)
	var seg []pubHop
	for _, h := range pub[entry:] {
		if h.fam != "" && h.fam != fam {
			res.Handoff = string(h.fam)
			break
		}
		seg = append(seg, h)
	}
	var core []pubHop
	for _, h := range seg {
		if !h.dst {
			core = append(core, h)
		}
	}
	res.Chain = chain(pub[entry:])

	var v verdict
	switch fam {
	case telecom:
		v = classifyTelecom(core)
	case unicom:
		v = classifyUnicom(core)
	default:
		v = classifyMobile(core)
	}
	res.Line, res.Tier, res.Confidence, res.Evidence = v.line, v.tier, v.conf, v.evidence

	// A path seen end to end, with no silent hop where it crosses into the
	// carrier, is what it shows. Otherwise a hidden hop could change the line.
	clear := reached && !gapBefore(hops, pub, entry)
	if res.Confidence == Confirmed && !clear {
		res.Confidence = Inferred
		if reached {
			res.Evidence += "；入境处有跳点未回应"
		} else {
			res.Evidence += "；追踪未到达目的地"
		}
	}
	return res
}

type verdict struct {
	line     string
	tier     int
	conf     string
	evidence string
}

func count(core []pubHop, pred func(pubHop) bool) (n, first, last int) {
	first, last = -1, -1
	for i, h := range core {
		if pred(h) {
			if first < 0 {
				first = i
			}
			last = i
			n++
		}
	}
	return n, first, last
}

func onNet(name string) func(pubHop) bool { return func(h pubHop) bool { return h.net == name } }

func classifyTelecom(core []pubHop) verdict {
	cn2, firstCN2, lastCN2 := count(core, onNet(netCN2))
	b163, _, _ := count(core, onNet(net163))
	if cn2 > 0 {
		// 163 ahead of CN2 counts even off its 202.97 range: those are the
		// 163 backbone's overseas routers (218.30.x and the like).
		pre163, _, _ := count(core[:firstCN2], func(h pubHop) bool { return h.asn == 4134 })
		mid163, _, _ := count(core[firstCN2:lastCN2+1], func(h pubHop) bool { return h.asn == 4134 })
		switch {
		case pre163 > 0:
			return verdict{"CN2 GT", TierQuality, Confirmed, "先经 163（AS4134）再进入 CN2"}
		case mid163 > 0 || b163 > 0:
			return verdict{"CN2 GT", TierQuality, Confirmed, "CN2 段中经过 163 骨干（202.97）"}
		case cn2 >= 2:
			return verdict{"CN2 GIA", TierPremium, Confirmed, fmt.Sprintf("全程走 CN2（%d 跳 59.43），未经过 163", cn2)}
		default:
			return verdict{"CN2", TierQuality, Insufficient, "只看到 1 跳 CN2，分不出 GIA 还是 GT"}
		}
	}
	if n, _, _ := count(core, onNet(netCTGNet)); n > 0 {
		return verdict{"CTGNet", TierPremium, Confirmed, "经电信国际网 CTGNet（AS23764）入境"}
	}
	if b163 > 0 {
		return verdict{"163", TierOrdinary, Confirmed, "经 163 骨干（202.97）入境"}
	}
	return verdict{"未见骨干", TierUnknown, Insufficient, "只看到电信省网地址，骨干段没有回应"}
}

func classifyUnicom(core []pubHop) verdict {
	n9929, first9929, _ := count(core, onNet(net9929))
	n169, first169, _ := count(core, onNet(net169))
	switch {
	case n9929 > 0 && n169 > 0 && first169 < first9929:
		return verdict{"9929 混合", TierQuality, Confirmed, "先经 4837 骨干（219.158）再进入 9929"}
	case n9929 > 0:
		return verdict{"9929", TierPremium, Confirmed, "经联通精品网 9929（AS9929）入境"}
	}
	if n, _, _ := count(core, onNet(netCUG)); n > 0 {
		return verdict{"CUG", TierQuality, Confirmed, "经联通国际网 CUG（AS10099）入境"}
	}
	if n169 > 0 {
		return verdict{"4837", TierOrdinary, Confirmed, "经 4837 骨干（219.158）入境"}
	}
	return verdict{"未见骨干", TierUnknown, Insufficient, "只看到联通省网地址，骨干段没有回应"}
}

func classifyMobile(core []pubHop) verdict {
	nCMIN2, firstCMIN2, _ := count(core, onNet(netCMIN2))
	nCMI, firstCMI, _ := count(core, onNet(netCMI))
	switch {
	case nCMIN2 > 0 && nCMI > 0 && firstCMI < firstCMIN2:
		return verdict{"CMIN2 混合", TierQuality, Confirmed, "先经 CMI（AS58453）再进入 CMIN2"}
	case nCMIN2 > 0:
		return verdict{"CMIN2", TierPremium, Confirmed, "经移动精品网 CMIN2（AS58807）入境"}
	case nCMI > 0:
		return verdict{"CMI", TierOrdinary, Confirmed, "经移动国际网 CMI（AS58453）入境"}
	}
	if n, _, _ := count(core, onNet(netCMNET)); n > 0 {
		return verdict{"CMNET", TierOrdinary, Confirmed, "经移动 CMNET 骨干（AS9808）入境"}
	}
	return verdict{"未见骨干", TierUnknown, Insufficient, "只看到移动省网地址，骨干段没有回应"}
}

// gapBefore reports a silent TTL between the last public hop ahead of the
// carrier and the first one inside it.
func gapBefore(hops []Hop, pub []pubHop, entry int) bool {
	from := 0
	if entry > 0 {
		from = pub[entry-1].index + 1
	}
	for _, h := range hops[from:pub[entry].index] {
		if h.IP == "" {
			return true
		}
	}
	return false
}

// chain compresses the carrier part of a route into the networks it crosses,
// e.g. 4134(163) → CN2 → 电信省网.
func chain(pub []pubHop) []string {
	var out []string
	for _, h := range pub {
		name := netName(h)
		if name == "" || (len(out) > 0 && out[len(out)-1] == name) {
			continue
		}
		out = append(out, name)
	}
	if out == nil {
		return []string{}
	}
	return out
}

func netName(h pubHop) string {
	switch h.net {
	case net163:
		return "4134(163)"
	case net169:
		return "4837(169)"
	case netCMNET:
		return "9808"
	case "":
	default:
		return h.net
	}
	switch h.asn {
	case 0:
		return ""
	case 4134, 4837, 9808:
		return fmt.Sprint(h.asn)
	}
	if h.fam != "" {
		return string(h.fam) + "省网"
	}
	return ""
}

// Comparable reports whether a change between two verdicts is a real change
// of line rather than a trace that saw less this time.
func Comparable(a, b Result) bool {
	return a.Confidence != Insufficient && b.Confidence != Insufficient &&
		!strings.HasPrefix(a.Line, "未") && !strings.HasPrefix(b.Line, "未")
}
