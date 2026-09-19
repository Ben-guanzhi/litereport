package auth

import (
	"strings"
	"testing"
)

func TestJWTSignVerify(t *testing.T) {
	m := NewManager("test-secret", 1, nil, nil)
	tok, err := m.Sign("admin", 0, "admin")
	if err != nil || strings.Count(tok, ".") != 2 {
		t.Fatalf("签发失败: %v %s", err, tok)
	}
	if u, ver, role, err := m.Verify(tok); err != nil || u != "admin" || ver != 0 || role != "admin" {
		t.Fatalf("校验失败: user=%q ver=%d role=%q err=%v", u, ver, role, err)
	}
	// 篡改
	if _, _, _, err := m.Verify(tok + "x"); err == nil {
		t.Error("篡改令牌应校验失败")
	}
	// 错误密钥
	m2 := NewManager("other-secret", 1, nil, nil)
	if _, _, _, err := m2.Verify(tok); err == nil {
		t.Error("其他密钥签发应校验失败")
	}
	// 过期令牌
	if _, _, _, err := m.Verify(buildExpired()); err == nil {
		t.Error("过期令牌应校验失败")
	}
	// 格式错误
	if _, _, _, err := m.Verify("abc.def.ghi"); err == nil {
		t.Error("格式错误令牌应失败")
	}
}

func TestPasswordVerify(t *testing.T) {
	m := NewManager("s", 1, []User{
		{Name: "admin", Password: "admin123", Role: "admin"},
		{Name: "bcrypt", Password: HashPassword("secret456"), Role: "editor"},
	}, nil)
	if u, role, _, err := m.VerifyPassword("admin", "admin123"); err != nil || u != "admin" || role != "admin" {
		t.Errorf("明文登录失败: user=%q role=%q err=%v", u, role, err)
	}
	if u, role, _, err := m.VerifyPassword("bcrypt", "secret456"); err != nil || u != "bcrypt" || role != "editor" {
		t.Errorf("bcrypt登录失败: user=%q role=%q err=%v", u, role, err)
	}
	if _, _, _, err := m.VerifyPassword("admin", "wrong"); err == nil {
		t.Error("错误密码应失败")
	}
	if _, _, _, err := m.VerifyPassword("nobody", "x"); err == nil {
		t.Error("不存在用户应失败")
	}
}

func TestAPIToken(t *testing.T) {
	m := NewManager("s", 1, nil, []string{"tok-1", "tok-2"})
	if !m.CheckAPIToken("tok-1") || m.CheckAPIToken("bad") || m.CheckAPIToken("") {
		t.Error("API Token 校验异常")
	}
}

func TestTokenVersionRevoke(t *testing.T) {
	m := NewManager("s", 1, []User{{Name: "u1", Password: "p"}}, nil)
	tok, _ := m.Sign("u1", 0, "viewer")
	if _, ver, _, err := m.Verify(tok); err != nil || ver != 0 {
		t.Fatalf("v0令牌校验失败: ver=%d err=%v", ver, err)
	}
	tok2, _ := m.Sign("u1", 1, "editor")
	if _, _, _, err := m.Verify(tok2); err != nil {
		t.Fatalf("v1令牌校验失败: %v", err)
	}
}

func buildExpired() string {
	m := NewManager("test-secret", 1, nil, nil)
	header := base64URL([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64URL([]byte(`{"sub":"admin","exp":1000000000}`))
	body := header + "." + payload
	return body + "." + base64URL(m.mac(body))
}
