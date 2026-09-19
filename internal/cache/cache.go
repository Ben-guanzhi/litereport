// Package cache 提供带 TTL 的进程内缓存，
// 用于报表配置、字段解析结果与 COUNT 结果等热点数据。
package cache

import (
	"container/list"
	"sync"
	"time"
)

// defaultMaxEntries 缓存条目上限：超过后先清过期键，仍满则 LRU 淘汰。
const defaultMaxEntries = 8192

// lruEntry LRU 双链表节点，同时持有键和值。
type lruEntry struct {
	key string
	val any
}

// Cache 简单的并发安全 TTL 缓存（LRU 淘汰 + TTL + 容量上限）。
type Cache struct {
	mu          sync.RWMutex
	max         int
	items       map[string]*list.Element // key -> list.Element (value holds lruEntry)
	lru         *list.List               // 存储 lruEntry 的双向链表（最近最少使用）
	expires     map[string]time.Time     // key -> 过期时间
}

// New 创建新缓存。
func New() *Cache {
	return &Cache{
		max:       defaultMaxEntries,
		items:     make(map[string]*list.Element),
		lru:       list.New(),
		expires:   make(map[string]time.Time),
	}
}

// Get 取值，过期或不存在返回 false。
func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	exp, hasExp := c.expires[key]
	if hasExp && time.Now().After(exp) {
		c.Delete(key)
		return nil, false
	}
	if hasExp {
		// 更新 LRU 位置（最近使用）
		c.mu.Lock()
		c.lru.MoveToFront(e)
		c.mu.Unlock()
	}
	// 从列表元素中取出值
	return e.Value.(lruEntry).val, true
}

// Set 写入并设置存活时长；达到容量上限时先清过期键，仍满则 LRU 淘汰。
func (c *Cache) Set(key string, v any, ttl time.Duration) {
	c.mu.Lock()
	if len(c.items) >= c.max {
		c.evictLocked()
	}
	// 移除旧节点（如果存在）
	if el, ok := c.items[key]; ok {
		c.lru.Remove(el)
		delete(c.items, key)
		delete(c.expires, key)
	}
	// 创建条目并存入列表
	entry := lruEntry{key: key, val: v}
	el := c.lru.PushFront(entry)
	c.items[key] = el
	el.Value = entry
	c.expires[key] = time.Now().Add(ttl)
	c.mu.Unlock()
}

// evictLocked 需在持有写锁时调用，按 LRU 顺序淘汰最久未使用的条目。
func (c *Cache) evictLocked() {
	now := time.Now()
	// 先移除所有过期条目
	for el := c.lru.Back(); el != nil; el = el.Prev() {
		e, ok := el.Value.(lruEntry)
		if !ok {
			continue
		}
		if exp, ok := c.expires[e.key]; ok && now.After(exp) {
			c.lru.Remove(el)
			delete(c.items, e.key)
			delete(c.expires, e.key)
		}
	}
	// 再如果仍超限，移除最少最近使用的
	for len(c.items) >= c.max && c.lru.Len() > 0 {
		el := c.lru.Back()
		if el == nil {
			break
		}
		e, ok := el.Value.(lruEntry)
		if !ok {
			break
		}
		c.lru.Remove(el)
		delete(c.items, e.key)
		delete(c.expires, e.key)
	}
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.items, key)
	delete(c.expires, key)
	c.mu.Unlock()
}

// DeletePrefix 批量失效前缀匹配的键（如配置变更时清除 "cfg:" 全部条目）。
func (c *Cache) DeletePrefix(prefix string) {
	c.mu.Lock()
	for k := range c.items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.items, k)
			delete(c.expires, k)
			// 从 LRU 列表中移除
			if el, ok := c.items[k]; ok {
				c.lru.Remove(el)
			}
		}
	}
	c.mu.Unlock()
}
