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
