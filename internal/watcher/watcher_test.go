package watcher

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func routesNamed(names ...string) []*model.Route {
	out := make([]*model.Route, 0, len(names))
	for i, name := range names {
		out = append(out, &model.Route{ID: int64(i + 1), Name: name, Enabled: true})
	}
	return out
}

// 这是这次修复的核心保证：一条链路的落地挂掉、探测卡满超时，不该让排在它
// 后面的链路一轮都轮不到。共用一整轮预算的旧写法正是这样把同一批链路反复
// 饿死的，而且因为链路按名字排序，饿死的永远是同样那几条。
func TestForEachConcurrentlySlowRouteDoesNotStarveOthers(t *testing.T) {
	routes := routesNamed("a-slow", "b", "c", "d", "e", "f", "g")

	var visited sync.Map
	forEachConcurrently(context.Background(), routes, 4, time.Second,
		func(ctx context.Context, route *model.Route) {
			if route.Name == "a-slow" {
				// 模拟落地不通：一直阻塞到自己的预算耗尽。
				<-ctx.Done()
			}
			visited.Store(route.Name, true)
		})

	for _, route := range routes {
		if _, ok := visited.Load(route.Name); !ok {
			t.Fatalf("链路 %s 这一轮没有被采样到", route.Name)
		}
	}
}

// 慢链路只该烧掉自己的预算。它的 context 到期时，别人的还得是活的。
func TestForEachConcurrentlyGivesEachRouteItsOwnBudget(t *testing.T) {
	routes := routesNamed("slow", "fast")

	var fastErr error
	forEachConcurrently(context.Background(), routes, 2, 150*time.Millisecond,
		func(ctx context.Context, route *model.Route) {
			if route.Name == "slow" {
				<-ctx.Done()
				return
			}
			// 等到慢的那条已经超时之后再检查自己的 context。
			time.Sleep(200 * time.Millisecond)
			fastErr = ctx.Err()
		})

	if fastErr == nil {
		t.Fatal("预算是每条独立的，这里本应看到 fast 自己也已到期")
	}
}

func TestForEachConcurrentlyRespectsLimit(t *testing.T) {
	routes := routesNamed("a", "b", "c", "d", "e", "f", "g", "h")

	var inFlight, peak int64
	forEachConcurrently(context.Background(), routes, 3, time.Second,
		func(ctx context.Context, route *model.Route) {
			n := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
		})

	if peak > 3 {
		t.Fatalf("并发数 = %d，超过了上限 3", peak)
	}
	if peak < 2 {
		t.Fatalf("并发数 = %d，说明根本没有并行起来", peak)
	}
}

// 关停时不该把已经起飞的采样丢在后面不管。
func TestForEachConcurrentlyWaitsForStartedWorkOnCancel(t *testing.T) {
	routes := routesNamed("a", "b", "c", "d")
	ctx, cancel := context.WithCancel(context.Background())

	var running, finished int64
	done := make(chan struct{})
	go func() {
		forEachConcurrently(ctx, routes, 2, time.Second,
			func(ctx context.Context, route *model.Route) {
				atomic.AddInt64(&running, 1)
				time.Sleep(60 * time.Millisecond)
				atomic.AddInt64(&finished, 1)
			})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后没有返回")
	}

	if got := atomic.LoadInt64(&finished); got != atomic.LoadInt64(&running) {
		t.Fatalf("已启动 %d 个，只等到 %d 个完成", running, got)
	}
}
