package routeclass

import (
	"strings"
	"testing"
)

func TestASNOf(t *testing.T) {
	cases := map[string]int{
		"59.43.182.1":  4809,  // CN2, unannounced, inside 4134's 59.32.0.0/13
		"59.36.1.1":    4134,  // Guangdong Telecom around it
		"202.97.12.5":  4134,  // 163
		"202.97.130.1": 4837,  // announced by Unicom despite the 202.97 look
		"219.158.3.1":  4837,  // 169, unannounced
		"218.105.2.1":  9929,  // 9929
		"69.194.161.1": 23764, // CTGNet, unannounced
		"8.8.8.8":      0,
		"not an ip":    0,
		"2001:db8::1":  0,
	}
	for ip, want := range cases {
		if got := ASNOf(ip); got != want {
			t.Errorf("%s: AS%d, want AS%d", ip, got, want)
		}
	}
	if PrefixDate() == "" {
		t.Error("prefix table carries no date")
	}
}

// trace builds an annotated trace from addresses; "*" is a silent hop.
func trace(ips ...string) []Hop {
	hops := make([]Hop, len(ips))
	for i, ip := range ips {
		hops[i] = Hop{TTL: i + 1}
		if ip != "*" {
			hops[i].IP = ip
		}
	}
	Annotate(hops)
	return hops
}

func TestClassify(t *testing.T) {
	const ctDst, cuDst, cmDst = "14.22.119.35", "112.90.1.1", "120.196.1.1"
	cases := []struct {
		name string
		hops []Hop
		dst  string
		line string
		conf string
	}{
		{"gia", trace("10.0.0.1", "154.1.1.1", "59.43.182.1", "59.43.246.9", "14.22.1.1", ctDst), ctDst, "CN2 GIA", Confirmed},
		{"gt, 163 after cn2", trace("154.1.1.1", "59.43.182.1", "59.43.1.9", "202.97.12.5", ctDst), ctDst, "CN2 GT", Confirmed},
		{"gt, 163 ahead of cn2", trace("154.1.1.1", "218.30.1.1", "59.43.1.1", "59.43.1.2", ctDst), ctDst, "CN2 GT", Confirmed},
		{"one cn2 hop", trace("154.1.1.1", "59.43.182.1", "14.22.1.1", ctDst), ctDst, "CN2", Insufficient},
		{"ctgnet", trace("154.1.1.1", "69.194.161.1", "202.97.1.1", ctDst), ctDst, "CTGNet", Confirmed},
		{"163", trace("154.1.1.1", "202.97.12.5", "202.97.1.9", ctDst), ctDst, "163", Confirmed},
		{"9929", trace("154.1.1.1", "218.105.2.1", "218.105.3.1", cuDst), cuDst, "9929", Confirmed},
		{"9929 mixed", trace("154.1.1.1", "219.158.3.1", "218.105.2.1", cuDst), cuDst, "9929 混合", Confirmed},
		{"4837", trace("154.1.1.1", "219.158.3.1", "219.158.9.1", cuDst), cuDst, "4837", Confirmed},
		{"cmnet", trace("154.1.1.1", "221.183.1.1", cmDst), cmDst, "CMNET", Confirmed},
		{"no carrier", trace("154.1.1.1", "8.8.8.8"), ctDst, "未识别", Insufficient},
		{"nothing answers", trace("*", "*"), ctDst, "未识别", Insufficient},
		{"unreached", trace("154.1.1.1", "202.97.12.5", "202.97.1.9", "*"), ctDst, "163", Inferred},
		{"gap at entry", trace("154.1.1.1", "*", "202.97.12.5", ctDst), ctDst, "163", Inferred},
	}
	for _, c := range cases {
		got := Classify(c.hops, c.dst)
		if got.Line != c.line || got.Confidence != c.conf {
			t.Errorf("%s: %s/%s (%s), want %s/%s", c.name, got.Line, got.Confidence, got.Evidence, c.line, c.conf)
		}
	}
}

func TestClassifyEntrySegmentOnly(t *testing.T) {
	const dst = "112.90.1.1"
	got := Classify(trace("154.1.1.1", "202.97.12.5", "202.97.1.9", "219.158.3.1", dst), dst)
	if got.Line != "163" || got.Entry != "电信" || got.Handoff != "联通" {
		t.Fatalf("%+v", got)
	}
	if strings.Join(got.Chain, " → ") != "4134(163) → 4837(169) → 联通省网" {
		t.Fatalf("chain %v", got.Chain)
	}
}

func TestDestinationIsNotEvidence(t *testing.T) {
	// The test point itself sitting in a 9929 block says nothing about the way in.
	got := Classify(trace("154.1.1.1", "219.158.3.1", "219.158.9.1", "218.105.2.1"), "218.105.2.1")
	if got.Line != "4837" {
		t.Fatalf("%+v", got)
	}
}

func TestComparable(t *testing.T) {
	gia := Result{Line: "CN2 GIA", Confidence: Confirmed}
	gt := Result{Line: "CN2 GT", Confidence: Inferred}
	weak := Result{Line: "CN2", Confidence: Insufficient}
	if !Comparable(gia, gt) || Comparable(gia, weak) || Comparable(Result{Line: "未识别", Confidence: Inferred}, gia) {
		t.Fatal("comparable")
	}
}
