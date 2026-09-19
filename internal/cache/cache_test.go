package cache

import (
	"testing"
	"time"
)

func TestCacheTTL(t *testing.T) {
	c := New()
	c.Set("k", "v", 50*time.Millisecond)
	if v, ok := c.Get("k"); !ok || v != "v" {
		t.Fatalf("Get = %v,%v", v, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("过期后应不可取")
	}
}

// TestCacheEviction 回归：条目达到上限时淘汰旧键，内存不无限增长。
func TestCacheEviction(t *testing.T) {
	c := New()
	for i := 0; i < 20000; i++ { // 远超默认 8192 上限
		c.Set("key"+string(rune(i%26))+string(rune(i)), i, time.Minute)
	}
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	if n > defaultMaxEntries {
		t.Fatalf("缓存条目 %d 超过上限 %d", n, defaultMaxEntries)
	}
}

func TestDeletePrefix(t *testing.T) {
	c := New()
	c.Set("cfg:a", 1, time.Minute)
	c.Set("cfg:b", 2, time.Minute)
	c.Set("other", 3, time.Minute)
	c.DeletePrefix("cfg:")
	if _, ok := c.Get("cfg:a"); ok {
		t.Fatal("cfg:a 应被清除")
	}
	if _, ok := c.Get("other"); !ok {
		t.Fatal("other 应保留")
	}
}