package ratelimit

import "testing"

func TestLimiterBurst(t *testing.T) {
	l := New(1, 5) // 每秒1个，桶容量5
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.Allow("ip1") {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("突发应放行5次, 实际 %d", allowed)
	}
	if l.Allow("ip2") != true {
		t.Error("不同键应独立限流")
	}
}
