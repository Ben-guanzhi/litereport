// Package auth 提供平台鉴权：登录用户校验（bcrypt/明文）、JWT 会话令牌（HS256，纯标准库）、API Token 校验。
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidToken = errors.New("令牌无效或已过期")
	ErrBadLogin     = errors.New("用户名或密码错误")
)

// User 登录用户。
type User struct {
	Name     string
	Password string // 明文或 bcrypt 哈希（$2 前缀自动识别）
	Role     string // admin / editor / viewer（空=默认）
}

// Manager 鉴权管理器。
type Manager struct {
	secret       []byte
	sessionHours int
	users        []User
	apiTokens    []string
	userLookup   func(name string) (hash string, ver int, role string, err error) // DB 用户（优先于配置用户）
}

func NewManager(secret string, sessionHours int, users []User, apiTokens []string) *Manager {
	if secret == "" {
		secret = "litereport-default-secret-change-me"
	}
	if sessionHours <= 0 {
		sessionHours = 24
	}
	return &Manager{
		secret:       []byte(secret),
		sessionHours: sessionHours,
		users:        users,
		apiTokens:    apiTokens,
	}
}

// SetUserLookup 注入 DB 用户查询（哈希非空表示该用户存于平台用户表）。
func (m *Manager) SetUserLookup(f func(name string) (hash string, ver int, role string, err error)) {
	m.userLookup = f
}

// lookupUser DB 优先返回 (hash, tokenVersion, role)；查不到回退配置用户。
func (m *Manager) lookupUser(name string) (hash string, ver int, role string) {
	if m.userLookup != nil {
		if h, v, r, err := m.userLookup(name); err == nil && h != "" {
			return h, v, r
		}
	}
	for _, u := range m.users {
		if u.Name == name {
			return u.Password, 0, u.Role
		}
	}
	return "", 0, ""
}

// VerifyPassword 校验登录，成功返回 (用户名, 角色, 会话版本)。
func (m *Manager) VerifyPassword(name, pass string) (string, string, int, error) {
	hash, ver, role := m.lookupUser(name)
	if hash != "" {
		if strings.HasPrefix(hash, "$2") { // bcrypt 哈希
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil {
				return name, role, ver, nil
			}
		} else if subtle.ConstantTimeCompare([]byte(hash), []byte(pass)) == 1 {
			return name, role, ver, nil
		}
		return "", "", 0, ErrBadLogin
	}
	_ = bcrypt.CompareHashAndPassword(
		[]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0X8G0gW6yLdQZtDq0EJtC8x6fOa"), []byte(pass))
	return "", "", 0, ErrBadLogin
}

// Sign 签发 HS256 JWT 会话令牌（携带会话版本，版本递增后旧令牌全部失效）。
func (m *Manager) Sign(username string, ver int, role string) (string, error) {
	now := time.Now()
	header := base64URL([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"sub":  username,
		"ver":  ver,
		"role": role,
		"iat":  now.Unix(),
		"exp":  now.Add(time.Duration(m.sessionHours) * time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	body := header + "." + base64URL(payload)
	return body + "." + base64URL(m.mac(body)), nil
}

// Verify 校验会话令牌，成功返回 (用户名, 会话版本, 角色)。
func (m *Manager) Verify(token string) (string, int, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0, "", ErrInvalidToken
	}
	if !hmac.Equal(m.mac(parts[0]+"."+parts[1]), unBase64(parts[2])) {
		return "", 0, "", ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, "", ErrInvalidToken
	}
	var claims struct {
		Sub  string `json:"sub"`
		Ver  int    `json:"ver"`
		Role string `json:"role"`
		Exp  int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Sub == "" {
		return "", 0, "", ErrInvalidToken
	}
	if time.Now().Unix() > claims.Exp {
		return "", 0, "", ErrInvalidToken
	}
	return claims.Sub, claims.Ver, claims.Role, nil
}

// CheckAPIToken 校验公共接口 API Token（恒时比较）。
func (m *Manager) CheckAPIToken(tok string) bool {
	if tok == "" || len(m.apiTokens) == 0 {
		return false
	}
	for _, t := range m.apiTokens {
		if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			return true
		}
	}
	return false
}

func (m *Manager) mac(s string) []byte {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unBase64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// HashPassword 生成 bcrypt 哈希（供运维生成配置用）。
func HashPassword(plain string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h)
}

// SecretString 返回签名密钥字符串（供数据源加密复用）。
func (m *Manager) SecretString() string { return string(m.secret) }

// EncryptDSN AES-GCM 加密 DSN（密钥由 secret 派生），返回 enc1: 前缀密文。
func EncryptDSN(plain, secret string) string {
	if plain == "" {
		return ""
	}
	key := deriveKey(secret)
	nonce := make([]byte, 12)
	_, _ = cryptorand.Read(nonce)
	block, err := aes.NewCipher(key)
	if err != nil {
		return plain
	}
	gcm, _ := cipher.NewGCM(block)
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return "enc1:" + base64.StdEncoding.EncodeToString(out)
}

// DecryptDSN 解密 enc1: 前缀密文；非密文原样返回。
func DecryptDSN(enc, secret string) string {
	if !strings.HasPrefix(enc, "enc1:") {
		return enc
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, "enc1:"))
	if err != nil || len(raw) < 12 {
		return ""
	}
	key := deriveKey(secret)
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte("litereport-dsn:" + secret))
	return sum[:]
}

// ConfigUsers 返回配置文件用户（角色含默认值）。
func (m *Manager) ConfigUsers() []User {
	out := make([]User, len(m.users))
	copy(out, m.users)
	return out
}
