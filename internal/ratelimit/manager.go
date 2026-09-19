// Redis 可选限流器：配置 redis_addr 后多实例共享限流窗口（固定窗口计数）。
// 未配置时由内存令牌桶接管（ratelimit.Limiter）。
package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLimiter 基于 Redis INCR+EXPIRE 的固定窗口限流。
type RedisLimiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
	prefix string
}

// NewRedisLimiter addr 形如 "127.0.0.1:6379"。
func NewRedisLimiter(addr, password string, db int, limitPerMin int, prefix string) (*RedisLimiter, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis连接失败: %w", err)
	}
	return &RedisLimiter{
		rdb: rdb, limit: limitPerMin,
		window: time.Minute, prefix: prefix,
	}, nil
}

// Allow 固定窗口计数：key = prefix:{分钟戳}:{维度}。
func (l *RedisLimiter) Allow(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	win := time.Now().Format("200601021504")
	rkey := fmt.Sprintf("%s:{%s}:%s", l.prefix, key, win)
	n, err := l.rdb.Incr(ctx, rkey).Result()
	if err != nil {
		return true // Redis 故障时放行（可用性优先），由内存限流兜底
	}
	if n == 1 {
		l.rdb.Expire(ctx, rkey, l.window+time.Second)
	}
	return n <= int64(l.limit)
}

// Manager 统一入口：Redis 可用时用 RedisLimiter，否则内存令牌桶。
type Manager struct {
	mu      sync.RWMutex
	memory  map[string]*Limiter
	redis   map[string]*RedisLimiter
	redisOn bool
	addr    string
	pass    string
	db      int
}

// NewManager 内存模式。
func NewManager() *Manager {
	return &Manager{memory: map[string]*Limiter{}, redis: map[string]*RedisLimiter{}}
}

// EnableRedis 全局启用 Redis 模式。
func (m *Manager) EnableRedis(addr, pass string, db int) error {
	probe, err := NewRedisLimiter(addr, pass, db, 1<<30, "litrpt:conn") // 连通性探测
	if err != nil {
		return err
	}
	_ = probe
	m.mu.Lock()
	m.redisOn, m.addr, m.pass, m.db = true, addr, pass, db
	m.mu.Unlock()
	return nil
}

// Bucket 取命名桶（内存令牌桶或 Redis 固定窗口）。
// memory 模式参数: ratePerSec, burst；redis 模式参数: limitPerMin。
func (m *Manager) Bucket(name string, memoryRatePerSec float64, memoryBurst int, redisLimitPerMin int) AllowFunc {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.redisOn {
		rl, ok := m.redis[name]
		if !ok {
			var err error
			rl, err = NewRedisLimiter(m.addr, m.pass, m.db, redisLimitPerMin, "litrpt:rl:"+name)
			if err != nil {
				ml := New(memoryRatePerSec, memoryBurst)
				m.memory[name] = ml
				return ml.Allow
			}
			m.redis[name] = rl
		}
		return rl.Allow
	}
	ml, ok := m.memory[name]
	if !ok {
		ml = New(memoryRatePerSec, memoryBurst)
		m.memory[name] = ml
	}
	return ml.Allow
}

// AllowFunc 判定函数。
type AllowFunc func(key string) bool
