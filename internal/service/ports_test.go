package service

import "testing"

func TestParseListeningPorts(t *testing.T) {
	// Real output shapes seen across ss and netstat on the supported distros.
	const out = `
0.0.0.0:22
[::]:22
*:8080
127.0.0.53%lo:53
10.0.0.14:68
[fe80::9365:b69a:5ab7:4ebe]%eth0:546
0.0.0.0:*
:::443
`
	got := parseListeningPorts(out)

	for _, want := range []int{22, 8080, 53, 68, 546, 443} {
		if !got[want] {
			t.Errorf("port %d not detected as in use", want)
		}
	}
	if len(got) != 6 {
		t.Errorf("detected %d ports, want 6: %v", len(got), got)
	}
}

func TestParseListeningPortsIgnoresGarbage(t *testing.T) {
	const out = `
Netid State  Recv-Q Send-Q Local Address:Port
0.0.0.0:*
not-an-address
:
0.0.0.0:99999
0.0.0.0:0
`
	got := parseListeningPorts(out)
	if len(got) != 0 {
		t.Errorf("parsed ports from garbage input: %v", got)
	}
}

// A wide pool starts allocating at 1, so failing to see sshd's port would
// hand realm a port it cannot bind.
func TestParseListeningPortsCatchesSSH(t *testing.T) {
	got := parseListeningPorts("0.0.0.0:22\n[::]:22")
	if !got[22] {
		t.Error("sshd port 22 not detected; a full-range pool would allocate it")
	}
}

// A DNAT rule diverts traffic before it reaches any socket, so a relay bound
// to that port would start cleanly and receive nothing. ss cannot see these,
// which is the whole reason the nat table is read.
func TestParseNATPorts(t *testing.T) {
	// Verbatim from managed hosts: docker publishing ports and a sing-box
	// manager's SB_DNAT chain, plus a redirect, a multiport rule and a range.
	// The SNAT companion rule carries the same --dport and must not be counted:
	// it describes the far side of the hop, not a claim on this machine.
	const out = `
-A DOCKER ! -i br-a685dbf5803f -p tcp -m tcp --dport 80 -j DNAT --to-destination 172.18.0.4:80
-A DOCKER ! -i br-a685dbf5803f -p tcp -m tcp --dport 443 -j DNAT --to-destination 172.18.0.4:443
-A SB_DNAT -p tcp -m tcp --dport 31001 -j DNAT --to-destination 112.105.152.213:31001
-A SB_DNAT -p udp -m udp --dport 31001 -j DNAT --to-destination 112.105.152.213:31001
-A SB_SNAT -d 112.105.152.213/32 -p tcp -m tcp --dport 31001 -j MASQUERADE
-A PREROUTING -p tcp -m tcp --dport 8080 -j REDIRECT --to-ports 3128
-A PREROUTING -p tcp -m multiport --dports 20000,20001,20005 -j DNAT --to-destination 10.0.0.2
-A PREROUTING -p tcp -m tcp --dport 30000:30004 -j DNAT --to-destination 10.0.0.3
-A POSTROUTING -s 172.18.0.0/16 -j MASQUERADE
`
	got := parseNATPorts(out)

	for _, want := range []int{80, 443, 31001, 8080, 20000, 20001, 20005, 30000, 30002, 30004} {
		if !got[want] {
			t.Errorf("port %d is diverted by a nat rule but was not claimed", want)
		}
	}
	// MASQUERADE redirects nothing, and the rewritten destination port is a
	// claim on the far side, not on this machine.
	for _, unwanted := range []int{3128, 30005, 20002} {
		if got[unwanted] {
			t.Errorf("port %d was claimed but no rule diverts it here", unwanted)
		}
	}
}

// A rule spanning most of the port space is a policy, not a claim on specific
// ports; expanding it would blank the pool and say nothing useful.
func TestParseNATPortsIgnoresHugeRanges(t *testing.T) {
	got := parseNATPorts(`-A PREROUTING -p tcp --dport 1:65535 -j DNAT --to-destination 10.0.0.9`)
	if len(got) != 0 {
		t.Errorf("a 65535-wide range produced %d claimed ports", len(got))
	}
}

func TestParseNATPortsIgnoresNonDivertingRules(t *testing.T) {
	const out = `
-A POSTROUTING -p tcp -m tcp --dport 31001 -j MASQUERADE
-A PREROUTING -p tcp -m tcp --dport 31002 -j ACCEPT
`
	if got := parseNATPorts(out); len(got) != 0 {
		t.Errorf("non-diverting rules claimed %v", got)
	}
}
