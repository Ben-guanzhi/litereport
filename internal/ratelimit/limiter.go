// Package ratelimit 提供进程内令牌桶限流（按维度键，如 IP）。
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter 令牌桶限流器：rate 为每秒补充令牌数，burst 为桶容量。
type Limiter struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	b     map[string]*bucket
	clock int64 // 惰性清理计数器
}

func New(ratePerSec float64, burst int) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1000
	}
	if burst <= 0 {
		burst = int(ratePerSec) * 2
	}
	return &Limiter{rate: ratePerSec, burst: float64(burst), b: map[string]*bucket{}}
}

// Allow 判断该键是否放行一次请求。
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock++
	if l.clock%4096 == 0 {
		l.sweep(now)
	}
	b, ok := l.b[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.b[key] = b
	}
	// 补充令牌
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// sweep 清理 10 分钟未活跃的桶，防止 map 无限增长。
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.b {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.b, k)
		}
	}
}
