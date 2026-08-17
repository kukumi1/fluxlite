package prober

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// metricsScript reads what a machine will tell us about itself without an agent
// installed on it.
//
// Output is key=value lines rather than fixed positions: a busybox without some
// file, or a container that hides a counter, then drops one line instead of
// shifting every field after it onto the wrong name. Anything absent stays
// absent, which the caller renders as unknown.
//
// It samples twice a second apart because CPU utilisation and interface rates
// are differences, not readings. There is no third option: /proc/stat is a
// monotonic counter, and dividing it by uptime would report the average since
// boot while claiming to report now.
const metricsScript = `
say() { printf '%s=%s\n' "$1" "$2"; }

c=$(nproc 2>/dev/null) || c=$(grep -c '^processor' /proc/cpuinfo 2>/dev/null) || c=""
[ -n "$c" ] && say cores "$c"

m=$(sed -n 's/^model name[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -1)
[ -z "$m" ] && m=$(sed -n 's/^Model[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -1)
[ -n "$m" ] && say model "$m"

k=$(uname -r 2>/dev/null) && [ -n "$k" ] && say kernel "$k"
u=$(cut -d' ' -f1 /proc/uptime 2>/dev/null) && [ -n "$u" ] && say uptime "$u"
l=$(cut -d' ' -f1,2,3 /proc/loadavg 2>/dev/null) && [ -n "$l" ] && say load "$l"

awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^MemFree:/{f=$2} /^Buffers:/{b=$2} /^Cached:/{ca=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2}
END{
  if (a == "") a = f + b + ca
  if (t != "") printf "memtotal=%.0f\nmemused=%.0f\n", t*1024, (t-a)*1024
  if (st != "" && st > 0) printf "swaptotal=%.0f\nswapused=%.0f\n", st*1024, (st-sf)*1024
}' /proc/meminfo 2>/dev/null

d=$(df -P -k / 2>/dev/null || df -k / 2>/dev/null)
[ -n "$d" ] && echo "$d" | awk 'END{ if ($2 ~ /^[0-9]+$/) printf "disktotal=%.0f\ndiskused=%.0f\n", $2*1024, $3*1024 }'

ct=""
if [ -f /.dockerenv ]; then ct=docker
elif [ -r /proc/1/environ ] && tr '\0' '\n' < /proc/1/environ 2>/dev/null | grep -q '^container='; then
  ct=$(tr '\0' '\n' < /proc/1/environ 2>/dev/null | sed -n 's/^container=//p' | head -1)
elif grep -qE '(docker|lxc|kubepods|libpod)' /proc/1/cgroup 2>/dev/null; then ct=container
fi
[ -n "$ct" ] && say container "$ct"

# 容器里 /proc 是宿主机的。cgroup 读得到就用它覆盖上面的内存读数，读不到就
# 保持 host 来源，由界面说明这个数字描述的不是这台容器。
if [ -r /sys/fs/cgroup/memory.max ]; then
  lim=$(cat /sys/fs/cgroup/memory.max 2>/dev/null)
  cur=$(cat /sys/fs/cgroup/memory.current 2>/dev/null)
  if [ -n "$cur" ] && [ "$lim" != "max" ] && [ -n "$lim" ]; then
    say memtotal "$lim"; say memused "$cur"; say memsource cgroup
  fi
elif [ -r /sys/fs/cgroup/memory/memory.limit_in_bytes ]; then
  lim=$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null)
  cur=$(cat /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null)
  if [ -n "$cur" ] && [ -n "$lim" ] && [ "$lim" -lt 9223372036854770000 ] 2>/dev/null; then
    say memtotal "$lim"; say memused "$cur"; say memsource cgroup
  fi
fi

read_cpu() { awk '/^cpu /{ i=$5; t=$2+$3+$4+$5+$6+$7+$8; print t, i }' /proc/stat 2>/dev/null; }
read_net() { awk 'NR>2 { sub(/:/, " "); split($0, f, " "); if (f[1] != "lo") { rx += f[2]; tx += f[10] } } END{ printf "%.0f %.0f\n", rx, tx }' /proc/net/dev 2>/dev/null; }

c1=$(read_cpu); n1=$(read_net)
sleep 1
c2=$(read_cpu); n2=$(read_net)

[ -n "$c1" ] && [ -n "$c2" ] && echo "$c1 $c2" | awk '{ dt=$3-$1; di=$4-$2; if (dt > 0) printf "cpu=%.1f\n", (1 - di/dt) * 100 }'
[ -n "$n1" ] && [ -n "$n2" ] && echo "$n1 $n2" | awk '{ printf "netrx=%.0f\nnettx=%.0f\nnetrxrate=%.0f\nnettxrate=%.0f\n", $3, $4, $3-$1, $4-$2 }'
`

// Metrics collects a resource snapshot over an established SSH connection.
func Metrics(ctx context.Context, client *ssh.Client) (*model.NodeMetrics, error) {
	out, err := sshx.RunCheck(ctx, client, metricsScript)
	if err != nil {
		return nil, fmt.Errorf("collect metrics: %w", err)
	}
	return parseMetrics(out), nil
}

// parseMetrics turns the script's output into a snapshot. A malformed or
// missing line leaves its field nil; it never fails the whole collection,
// because one unreadable counter is not a reason to discard the nine that
// were read fine.
func parseMetrics(out string) *model.NodeMetrics {
	m := &model.NodeMetrics{
		MemSource:   model.MemSourceHost,
		CollectedAt: time.Now().UTC(),
	}

	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case "cores":
			m.Cores = parseIntPtr(value)
		case "model":
			m.CPUModel = value
		case "kernel":
			m.Kernel = value
		case "uptime":
			if f, err := strconv.ParseFloat(value, 64); err == nil && f >= 0 {
				secs := int64(f)
				m.UptimeSec = &secs
			}
		case "load":
			parseLoad(value, m)
		case "memtotal":
			m.MemTotal = parseInt64Ptr(value)
		case "memused":
			m.MemUsed = parseInt64Ptr(value)
		case "swaptotal":
			m.SwapTotal = parseInt64Ptr(value)
		case "swapused":
			m.SwapUsed = parseInt64Ptr(value)
		case "disktotal":
			m.DiskTotal = parseInt64Ptr(value)
		case "diskused":
			m.DiskUsed = parseInt64Ptr(value)
		case "memsource":
			if value == string(model.MemSourceCgroup) {
				m.MemSource = model.MemSourceCgroup
			}
		case "container":
			m.Container = value
		case "cpu":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				// 采样窗口内计数器回绕或被容器改写都会算出界外的百分比，
				// 夹回 0-100 好过在界面上画出 -300% 的进度条。
				m.CPUPercent = clampPercent(f)
			}
		case "netrx":
			m.NetRxBytes = parseInt64Ptr(value)
		case "nettx":
			m.NetTxBytes = parseInt64Ptr(value)
		case "netrxrate":
			m.NetRxRate = nonNegative(parseInt64Ptr(value))
		case "nettxrate":
			m.NetTxRate = nonNegative(parseInt64Ptr(value))
		}
	}

	// 用量大于总量说明两个数来自不同的世界（最典型的是容器只读到一半的
	// cgroup），与其画一条 340% 的进度条，不如两个都不报。
	if m.MemTotal != nil && m.MemUsed != nil && (*m.MemUsed > *m.MemTotal || *m.MemTotal <= 0) {
		m.MemTotal, m.MemUsed = nil, nil
	}
	if m.DiskTotal != nil && m.DiskUsed != nil && (*m.DiskUsed > *m.DiskTotal || *m.DiskTotal <= 0) {
		m.DiskTotal, m.DiskUsed = nil, nil
	}
	return m
}

func parseLoad(value string, m *model.NodeMetrics) {
	parts := strings.Fields(value)
	if len(parts) != 3 {
		return
	}
	m.Load1 = parseFloatPtr(parts[0])
	m.Load5 = parseFloatPtr(parts[1])
	m.Load15 = parseFloatPtr(parts[2])
}

func parseIntPtr(s string) *int {
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return nil
	}
	return &v
}

func parseInt64Ptr(s string) *int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseFloatPtr(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return nil
	}
	return &v
}

func clampPercent(f float64) *float64 {
	if f < 0 {
		f = 0
	}
	if f > 100 {
		f = 100
	}
	return &f
}

// nonNegative drops a rate computed across a counter reset, which would
// otherwise show as a large negative transfer.
func nonNegative(v *int64) *int64 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}
