package ratelimit

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestRedisLimiter(t *testing.T) {
	mr := miniredis.RunT(t)
	l, err := NewRedisLimiter(mr.Addr(), "", 0, 3, "t")
	if err != nil {
		t.Fatal(err)
	}
	results := []bool{}
	for i := 0; i < 5; i++ {
		results = append(results, l.Allow("ip1"))
	}
	allowed := 0
	for _, r := range results {
		if r {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("固定窗口应放行3次, 实际 %d", allowed)
	}
	if !l.Allow("ip2") {
		t.Error("不同key应独立计数")
	}
}

func TestManagerBuckets(t *testing.T) {
	m := NewManager()
	mem := m.Bucket("pub", 1000, 10, 0)
	n := 0
	for i := 0; i < 15; i++ {
		if mem("k") {
			n++
		}
	}
	if n != 10 {
		t.Errorf("内存桶应放行10次, 实际 %d", n)
	}

	mr := miniredis.RunT(t)
	if err := m.EnableRedis(mr.Addr(), "", 0); err != nil {
		t.Fatal(err)
	}
	rd := m.Bucket("pub", 1000, 10, 4)
	n = 0
	for i := 0; i < 10; i++ {
		if rd("k2") {
			n++
		}
	}
	if n != 4 {
		t.Errorf("redis桶应放行4次, 实际 %d", n)
	}
}
