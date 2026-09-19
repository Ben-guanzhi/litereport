package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientIPUntrustedXFF 回归：默认直连部署不信任 X-Forwarded-For，防伪造绕过限流。
func TestClientIPUntrustedXFF(t *testing.T) {
	s := &Server{trustedXFF: map[string]bool{}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := s.clientIP(r); got != "203.0.113.7" {
		t.Errorf("直连场景应取 RemoteAddr，got %q", got)
	}
}

// TestClientIPTrustedProxy 可信代理命中时取 XFF 首段。
func TestClientIPTrustedProxy(t *testing.T) {
	s := &Server{trustedXFF: map[string]bool{"127.0.0.1": true}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:8080"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	if got := s.clientIP(r); got != "1.2.3.4" {
		t.Errorf("可信代理应取 XFF 首段，got %q", got)
	}
}

// TestSafeNext 登录跳转目标仅允许站内相对路径，防开放重定向。
func TestSafeNext(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/index.html", "/index.html"},
		{"/cgreport/demo?a=1", "/cgreport/demo?a=1"},
		{"https://evil.com", "/"},
		{"//evil.com", "/"},
		{"/\\evil.com", "/"},
		{"", "/"},
		{"index.html", "/"},
	}
	for _, c := range cases {
		if got := safeNext(c.in); got != c.want {
			t.Errorf("safeNext(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
