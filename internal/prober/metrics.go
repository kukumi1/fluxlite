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

ct=""
if [ -f /.dockerenv ]; then ct=docker
elif [ -r /proc/1/environ ] && tr '\0' '\n' < /proc/1/environ 2>/dev/null | grep -q '^container='; then
  ct=$(tr '\0' '\n' < /proc/1/environ 2>/dev/null | sed -n 's/^container=//p' | head -1)
elif grep -qE '(docker|lxc|kubepods|libpod)' /proc/1/cgroup 2>/dev/null; then ct=container
fi
[ -n "$ct" ] && say container "$ct"

# 多数容器看到的 /proc/uptime、/proc/loadavg、/proc/stat 是宿主机的；只有 lxcfs
# 把它们挂成容器自己的版本。读之前先分清，否则显示的是整台宿主机的开机时长、
# 负载和 CPU，和这台容器毫无关系。
own_view() { [ -z "$ct" ] || awk -v p="$1" '$2 == p && $3 ~ /lxcfs/ { f = 1 } END { exit !f }' /proc/mounts 2>/dev/null; }

k=$(uname -r 2>/dev/null) && [ -n "$k" ] && say kernel "$k"
u=$(cut -d' ' -f1 /proc/uptime 2>/dev/null)
if [ -n "$u" ] && ! own_view /proc/uptime; then
  # 容器的开机时刻就是它 1 号进程的启动时刻，记在 /proc/1/stat 第 22 个字段，
  # 单位是从宿主机开机起的时钟滴答。进程名可能带空格，所以从右括号之后数起。
  st=$(sed 's/^.*) //' /proc/1/stat 2>/dev/null | awk '{ print $20 }')
  hz=$(getconf CLK_TCK 2>/dev/null) || hz=100
  u=$(awk -v u="$u" -v s="$st" -v hz="${hz:-100}" 'BEGIN { if (s != "" && hz > 0 && u - s/hz > 0) printf "%.0f", u - s/hz }')
fi
[ -n "$u" ] && say uptime "$u"
# 负载没有按容器拆分的来源，拿不到容器自己的就不报：宿主机的负载只会误导。
if own_view /proc/loadavg; then
  l=$(cut -d' ' -f1,2,3 /proc/loadavg 2>/dev/null) && [ -n "$l" ] && say load "$l"
fi

awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^MemFree:/{f=$2} /^Buffers:/{b=$2} /^Cached:/{ca=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2}
END{
  if (a == "") a = f + b + ca
  if (t != "") printf "memtotal=%.0f\nmemused=%.0f\n", t*1024, (t-a)*1024
  if (st != "" && st > 0) printf "swaptotal=%.0f\nswapused=%.0f\n", st*1024, (st-sf)*1024
}' /proc/meminfo 2>/dev/null

d=$(df -P -k / 2>/dev/null || df -k / 2>/dev/null)
[ -n "$d" ] && echo "$d" | awk 'END{ if ($2 ~ /^[0-9]+$/) printf "disktotal=%.0f\ndiskused=%.0f\n", $2*1024, $3*1024 }'

# 容器里 /proc 是宿主机的。cgroup 读得到就用它覆盖上面的内存读数，读不到就
# 保持 host 来源，由界面说明这个数字描述的不是这台容器。
#
# cgroup 的用量把文件缓存也算在内。小容器读过的文件很快把额度填满，用量常年
# 贴着上限，看上去像内存耗尽，其实程序只用了一小部分。和非容器机器用的
# MemTotal - MemAvailable 保持同一口径：文件缓存可回收，扣掉；共享内存虽然记在
# 文件缓存里却回收不了，加回来。结果和容器里 free 显示的 used 一致。
stat_value() { awk -v k="$1" '$1 == k { print $2; exit }' "$2" 2>/dev/null; }
in_use() {
  fc=$(stat_value "$2" "$4"); sh=$(stat_value "$3" "$4")
  if [ -n "$fc" ] && [ "$fc" -le "$1" ] 2>/dev/null; then echo $(($1 - fc + ${sh:-0})); else echo "$1"; fi
}
if [ -r /sys/fs/cgroup/memory.max ]; then
  lim=$(cat /sys/fs/cgroup/memory.max 2>/dev/null)
  cur=$(cat /sys/fs/cgroup/memory.current 2>/dev/null)
  [ -n "$cur" ] && cur=$(in_use "$cur" file shmem /sys/fs/cgroup/memory.stat)
  if [ -n "$cur" ] && [ "$lim" != "max" ] && [ -n "$lim" ]; then
    say memtotal "$lim"; say memused "$cur"; say memsource cgroup
  fi
elif [ -r /sys/fs/cgroup/memory/memory.limit_in_bytes ]; then
  lim=$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null)
  cur=$(cat /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null)
  [ -n "$cur" ] && cur=$(in_use "$cur" total_cache total_shmem /sys/fs/cgroup/memory/memory.stat)
  if [ -n "$cur" ] && [ -n "$lim" ] && [ "$lim" -lt 9223372036854770000 ] 2>/dev/null; then
    say memtotal "$lim"; say memused "$cur"; say memsource cgroup
  fi
fi

# iowait 记作空闲：那段时间 CPU 没在干活，只是有进程在等磁盘。算成占用的话，
# 磁盘忙的机器 CPU 会虚高好几倍，top 和 htop 也都不把它算进使用率。steal 是
# 被宿主机拿走的时间，计入总数但不算本机的占用。
read_cpu() { awk '/^cpu /{ i=$5+$6; t=$2+$3+$4+$5+$6+$7+$8+$9; print t, i }' /proc/stat 2>/dev/null; }
read_net() { awk 'NR>2 { sub(/:/, " "); split($0, f, " "); if (f[1] != "lo") { rx += f[2]; tx += f[10] } } END{ printf "%.0f %.0f\n", rx, tx }' /proc/net/dev 2>/dev/null; }

# 容器的 CPU 用量记在 cgroup 里（纳秒），按它被分到的核数折成百分比。有配额
# 就按配额算，没有就按 nproc，它已经反映了 cpuset 的限制。
cg_usage() {
  if [ -r /sys/fs/cgroup/cpu.stat ]; then
    awk '$1 == "usage_usec" { printf "%.0f", $2 * 1000; exit }' /sys/fs/cgroup/cpu.stat
  else
    for f in /sys/fs/cgroup/cpuacct/cpuacct.usage /sys/fs/cgroup/cpu,cpuacct/cpuacct.usage; do
      [ -r "$f" ] && { cat "$f"; return; }
    done
  fi
}
cg_quota() {
  if [ -r /sys/fs/cgroup/cpu.max ]; then
    awk '$1 != "max" && $2 > 0 { printf "%.3f", $1 / $2 }' /sys/fs/cgroup/cpu.max
  else
    for d in /sys/fs/cgroup/cpu /sys/fs/cgroup/cpu,cpuacct; do
      [ -r "$d/cpu.cfs_quota_us" ] || continue
      awk -v q="$(cat "$d/cpu.cfs_quota_us")" -v p="$(cat "$d/cpu.cfs_period_us")" 'BEGIN { if (q > 0 && p > 0) printf "%.3f", q / p }'
      return
    done
  fi
}
wall() { cut -d' ' -f1 /proc/uptime 2>/dev/null; }

g1=""; g2=""
own_view /proc/stat || g1=$(cg_usage 2>/dev/null)
c1=$(read_cpu); n1=$(read_net); w1=$(wall)
sleep 1
c2=$(read_cpu); n2=$(read_net); w2=$(wall)
[ -n "$g1" ] && g2=$(cg_usage 2>/dev/null)

if [ -n "$g1" ] && [ -n "$g2" ]; then
  q=$(cg_quota 2>/dev/null)
  [ -n "$q" ] && say cores "$(awk -v q="$q" 'BEGIN { n = int(q); if (n < q) n++; print n }')"
  awk -v a="$g1" -v b="$g2" -v w1="$w1" -v w2="$w2" -v cap="${q:-${c:-1}}" \
    'BEGIN { dt = w2 - w1; if (dt > 0 && cap > 0) printf "cpu=%.1f\n", (b - a) / 1e9 / dt / cap * 100 }'
elif [ -n "$c1" ] && [ -n "$c2" ]; then
  echo "$c1 $c2" | awk '{ dt=$3-$1; di=$4-$2; if (dt > 0) printf "cpu=%.1f\n", (1 - di/dt) * 100 }'
fi
[ -n "$n1" ] && [ -n "$n2" ] && echo "$n1 $n2" | awk '{ printf "netrx=%.0f\nnettx=%.0f\nnetrxrate=%.0f\nnettxrate=%.0f\n", $3, $4, $3-$1, $4-$2 }'
`

// Metrics collects a resource snapshot over an established SSH connection.
func Metrics(ctx context.Context, client *ssh.Client) (*model.NodeMetrics, error) {
	// Only a failed session is an error. The script's exit status is that of
	// whichever reading came last, and one unreadable counter must not make a
	// healthy machine look unreachable to the caller deciding just that.
	res, err := sshx.Run(ctx, client, metricsScript)
	if err != nil {
		return nil, fmt.Errorf("collect metrics: %w", err)
	}
	return parseMetrics(res.Stdout), nil
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
