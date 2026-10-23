package service

import (
	"errors"
	"testing"
)

// 机器重启或防火墙重载后计数规则没了：要补回规则再读，而不是一直读失败、
// 让这个节点的流量从此不再计入额度。
func TestMissingAccountingRulesAreRestoredAndResampled(t *testing.T) {
	samples, repairs := 0, 0
	sample := func() ([2]int64, error) {
		samples++
		if repairs == 0 {
			return [2]int64{}, errSingBoxAccountingMissing
		}
		return [2]int64{10, 20}, nil
	}
	got, err := sampleWithRepair(sample, func() error { repairs++; return nil })
	if err != nil || got != [2]int64{10, 20} || repairs != 1 || samples != 2 {
		t.Fatalf("应补回规则后重新读到计数: %v %v repairs=%d samples=%d", got, err, repairs, samples)
	}
}

func TestOtherAccountingFailuresAreNotRepaired(t *testing.T) {
	unreachable := errors.New("accounting chain unavailable")
	_, err := sampleWithRepair(func() ([2]int64, error) { return [2]int64{}, unreachable },
		func() error { t.Fatal("读不到不等于规则丢了，不能去改防火墙"); return nil })
	if !errors.Is(err, unreachable) {
		t.Fatalf("原错误要原样返回: %v", err)
	}
}

func TestFailedRestoreIsReported(t *testing.T) {
	_, err := sampleWithRepair(func() ([2]int64, error) { return [2]int64{}, errSingBoxAccountingMissing },
		func() error { return errors.New("iptables: Permission denied") })
	if !errors.Is(err, errSingBoxAccountingMissing) || err.Error() == errSingBoxAccountingMissing.Error() {
		t.Fatalf("补规则失败要连同原因一起报出来: %v", err)
	}
}
