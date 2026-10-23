// Package audit turns sing-box access logs into "who connected, and what did
// they reach" records.
//
// The visited site is only knowable where the proxy protocol is decrypted, so
// every record originates from the log of the sing-box serving the node.
package audit

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Direction is which half of a connection a log line describes.
type Direction int

const (
	// From carries the peer that connected to the inbound.
	From Direction = iota + 1
	// To carries the destination the client asked the proxy to reach.
	To
)

// Event is one parsed "inbound connection from|to" line.
type Event struct {
	At     time.Time // zero when the line carried no timestamp
	ConnID uint64
	Dir    Direction
	Addr   string
	User   string // sing-box user name, when the inbound has named users
	// Inbound and InboundType name the inbound the connection arrived on;
	// matched against the process's config, they give its listen port.
	Inbound     string
	InboundType string
	// Packet marks a UDP association rather than a TCP connection.
	Packet bool
}

// sing-box tags every line of a connection with the same random id, which is
// the only thing tying the client address to the destination.
var eventPattern = regexp.MustCompile(
	`\[(\d+) [^\]]*\] inbound/([\w-]+)\[([^\]]*)\]: (?:\[([^\]]*)\] )?inbound (packet )?connection (from|to) (\S+)\s*$`)

// fileTimestamp is sing-box's own prefix when it writes to a file with
// timestamps on.
var fileTimestamp = regexp.MustCompile(`^([+-]\d{4} \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s`)

// ansiColour matches terminal colour codes. sing-box colours its console
// output, and OpenRC captures that output into a file as it is.
var ansiColour = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ParseLine extracts an Event from a sing-box log line, whether it came from
// a log file or from journalctl -o short-iso-precise.
func ParseLine(line string) (Event, bool) {
	if strings.IndexByte(line, 0x1b) >= 0 {
		line = ansiColour.ReplaceAllString(line, "")
	}
	m := eventPattern.FindStringSubmatch(line)
	if m == nil {
		return Event{}, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return Event{}, false
	}
	ev := Event{ConnID: id, InboundType: m[2], Inbound: m[3], User: m[4], Packet: m[5] != "", Addr: m[7], At: lineTime(line)}
	if m[6] == "from" {
		ev.Dir = From
	} else {
		ev.Dir = To
	}
	return ev, true
}

func lineTime(line string) time.Time {
	if first, _, ok := strings.Cut(line, " "); ok && len(first) > 10 && first[4] == '-' && first[10] == 'T' {
		if t, err := time.Parse(time.RFC3339Nano, first); err == nil {
			return t
		}
	}
	if m := fileTimestamp.FindStringSubmatch(line); m != nil {
		if t, err := time.Parse("-0700 2006-01-02 15:04:05", m[1]); err == nil {
			return t
		}
	}
	return time.Time{}
}

// HostOf returns the host part of a host:port, bracketed IPv6 included.
func HostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// Conn is a connection whose client and destination have both been seen.
type Conn struct {
	At          time.Time
	From        string // client address as the logging machine saw it
	To          string
	User        string
	Inbound     string
	InboundType string
	Packet      bool
}

// pendingTTL bounds how long a "from" waits for its "to". sing-box writes the
// pair within milliseconds; anything older is a connection that never asked
// for a destination — the panel's own latency probes are exactly that.
const pendingTTL = time.Minute

type pending struct {
	from string
	user string
	seen time.Time
}

// Correlator pairs the from and to lines of each connection from one log.
// It is not safe for concurrent use.
type Correlator struct {
	pending map[uint64]pending
}

func NewCorrelator() *Correlator {
	return &Correlator{pending: make(map[uint64]pending)}
}

// Feed consumes one event and returns a Conn once its destination is known.
// A destination whose "from" was never seen still yields a Conn with an empty
// From: what was reached is known even when who reached it is not.
func (c *Correlator) Feed(ev Event, now time.Time) (Conn, bool) {
	switch ev.Dir {
	case From:
		c.pending[ev.ConnID] = pending{from: ev.Addr, user: ev.User, seen: now}
		return Conn{}, false
	case To:
		p, ok := c.pending[ev.ConnID]
		delete(c.pending, ev.ConnID)
		at := ev.At
		if at.IsZero() {
			at = now
		}
		user := ev.User
		if user == "" && ok {
			user = p.user
		}
		return Conn{At: at, From: p.from, To: ev.Addr, User: user, Inbound: ev.Inbound, InboundType: ev.InboundType, Packet: ev.Packet}, true
	}
	return Conn{}, false
}

// Prune drops "from" lines that never got a destination.
func (c *Correlator) Prune(now time.Time) {
	for id, p := range c.pending {
		if now.Sub(p.seen) > pendingTTL {
			delete(c.pending, id)
		}
	}
}
