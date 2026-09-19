package service

import "testing"

// TestFmtCell 回归：仅 ISO 日期时间的 T 替换为空格，普通含 T 字符串不受影响。
func TestFmtCell(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"2026-01-02T15:04:05", "2026-01-02 15:04:05"},
		{"2026-01-02T15:04", "2026-01-02 15:04"},
		{"2026-01-02t15:04:05.123", "2026-01-02 15:04:05.123"},
		{"2026-01-02T15:04:05Z", "2026-01-02 15:04:05Z"},
		{"2026-01-02T15:04:05+08:00", "2026-01-02 15:04:05+08:00"},
		{"Top10", "Top10"},   // 曾被误替换为 " op10"
		{"TCP/IP", "TCP/IP"}, // 普通字符串原样保留
		{"not-a-datetime", "not-a-datetime"},
		{"2026-01-02", "2026-01-02"}, // 纯日期无 T
		{"", ""},
		{nil, ""},
		{int64(42), "42"},
		{3.14, "3.14"},
		{true, "true"},
	}
	for _, c := range cases {
		if got := fmtCell(c.in); got != c.want {
			t.Errorf("fmtCell(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
