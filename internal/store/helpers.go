package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewID 导出的随机 ID 生成器（32位十六进制）。
func NewID() string { return newID() }

// nowArg 返回当前时间；sqlite 时间戳列接受任意驱动值。
func nowArg() any { return time.Now() }
