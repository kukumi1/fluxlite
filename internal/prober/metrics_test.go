package prober

import (
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestParseMetricsFullSample(t *testing.T) {
	m := parseMetrics(`cores=4
model=AMD EPYC 7B13
kernel=6.1.0-18-amd64
uptime=1049321.55
load=0.42 0.38 0.31
memtotal=4127866880
memused=2394451968
swaptotal=1073741824
swapused=134217728
disktotal=21474836480
diskused=5476083712
cpu=31.4
netrx=983412775
nettx=412998211
netrxrate=51200
nettxrate=20480
`)

	if m.Cores == nil || *m.Cores != 4 {
		t.Fatalf("cores = %v", m.Cores)
	}
	if m.CPUModel != "AMD EPYC 7B13" {
		t.Fatalf("model = %q", m.CPUModel)
	}
	if m.UptimeSec == nil || *m.UptimeSec != 1049321 {
		t.Fatalf("uptime = %v", m.UptimeSec)
	}
	if m.Load1 == nil || *m.Load1 != 0.42 || m.Load15 == nil || *m.Load15 != 0.31 {
		t.Fatalf("load = %v %v %v", m.Load1, m.Load5, m.Load15)
	}
	if m.CPUPercent == nil || *m.CPUPercent != 31.4 {
		t.Fatalf("cpu = %v", m.CPUPercent)
	}
	if m.MemSource != model.MemSourceHost {
		t.Fatalf("mem source = %q, want host by default", m.MemSource)
	}
	if m.NetRxRate == nil || *m.NetRxRate != 51200 {
		t.Fatalf("rx rate = %v", m.NetRxRate)
	}
}

// 缺行不该让整次采集作废：busybox 没有的文件、容器藏起来的计数器，都只该
// 让那一个字段变成未知。
func TestParseMetricsToleratesMissingLines(t *testing.T) {
	m := parseMetrics("cores=2\nkernel=5.15.0\n")

	if m.Cores == nil || *m.Cores != 2 {
		t.Fatalf("cores = %v", m.Cores)
	}
	if m.MemTotal != nil || m.CPUPercent != nil || m.DiskTotal != nil {
		t.Fatal("缺失的字段应当为 nil，而不是零值")
	}
	if m.Load1 != nil {
		t.Fatal("没有 load 行时不该编出一个 0")
	}
}

// cgroup 行出现在 meminfo 之后，必须覆盖它 —— 容器里 /proc/meminfo 是宿主机的。
func TestParseMetricsCgroupOverridesHostMemory(t *testing.T) {
	m := parseMetrics(`memtotal=67553future
memtotal=67553range
memtotal=8589934592
memused=6442450944
container=podman
memtotal=536870912
memused=209715200
memsource=cgroup
`)

	if m.MemSource != model.MemSourceCgroup {
		t.Fatalf("mem source = %q", m.MemSource)
	}
	if m.MemTotal == nil || *m.MemTotal != 536870912 {
		t.Fatalf("mem total = %v, want the cgroup limit", m.MemTotal)
	}
	if m.MemUsed == nil || *m.MemUsed != 209715200 {
		t.Fatalf("mem used = %v", m.MemUsed)
	}
	if m.Container != "podman" {
		t.Fatalf("container = %q", m.Container)
	}
}

// 用量超过总量说明两个数来自不同的世界，与其画一条 340% 的条，不如都不报。
func TestParseMetricsDropsImpossiblePairs(t *testing.T) {
	m := parseMetrics("memtotal=1000\nmemused=3400\ndisktotal=0\ndiskused=0\n")

	if m.MemTotal != nil || m.MemUsed != nil {
		t.Fatalf("矛盾的内存读数应当整对丢弃: %v %v", m.MemTotal, m.MemUsed)
	}
	if m.DiskTotal != nil || m.DiskUsed != nil {
		t.Fatalf("总量为 0 的磁盘读数应当丢弃: %v %v", m.DiskTotal, m.DiskUsed)
	}
}

// 计数器归零会让速率算成负数，那不是「传了负的字节」，是「不知道」。
func TestParseMetricsDropsNegativeRates(t *testing.T) {
	m := parseMetrics("netrxrate=-8192\nnettxrate=4096\ncpu=-12\n")

	if m.NetRxRate != nil {
		t.Fatalf("负速率应当为 nil, got %v", *m.NetRxRate)
	}
	if m.NetTxRate == nil || *m.NetTxRate != 4096 {
		t.Fatalf("tx rate = %v", m.NetTxRate)
	}
	if m.CPUPercent == nil || *m.CPUPercent != 0 {
		t.Fatalf("越界的 CPU 百分比应当夹到 0, got %v", m.CPUPercent)
	}
}
