// Package config 负责加载并规范化平台配置。
// 平台元数据(报表配置本身)与业务数据源均支持 sqlite/mysql/postgresql/oracle。
package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Server struct {
	Port int    `yaml:"port"`
	Host string `yaml:"host"`
}

// Meta 元数据库配置：存放报表配置(头/字段/参数)。
type Meta struct {
	Type string `yaml:"type"` // sqlite | mysql | postgresql | oracle
	DSN  string `yaml:"dsn"`
}

// DbPool 连接池配置（对 sqlite 强制单连接，配置仅对网络库生效）。
type DbPool struct {
	MaxOpen            int `yaml:"max_open"`
	MaxIdle            int `yaml:"max_idle"`
	MaxLifetimeMinutes int `yaml:"max_lifetime_minutes"`
	MaxIdleTimeMinutes int `yaml:"max_idle_time_minutes"`
}

// DS 动态数据源配置：报表实际查询/保存数据的库。
type DS struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	DSN  string `yaml:"dsn"`
}

// UserCfg 平台登录用户。Password 支持明文或 bcrypt 哈希（$2 前缀自动识别）。
type UserCfg struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
	Role     string `yaml:"role"` // admin / editor / viewer，缺省 admin
}

// AuthCfg 鉴权配置：管理端会话(JWT Cookie) + 公共接口 API Token。
type AuthCfg struct {
	Enabled      *bool     `yaml:"enabled"` // 缺省 true（安全默认）
	Secret       string    `yaml:"secret"`  // JWT 签名密钥，空则使用内置默认（生产请修改）
	SessionHours int       `yaml:"session_hours"`
	Users        []UserCfg `yaml:"users"`
	APITokens    []string  `yaml:"api_tokens"`
}

// QueryCfg 查询执行配置。
type QueryCfg struct {
	TimeoutSeconds int `yaml:"timeout_seconds"` // 单次查询超时，<=0 表示不限
}

// RateLimitCfg 限流配置（令牌桶，按 IP）。
type RateLimitCfg struct {
	PublicRPS   float64 `yaml:"public_rps"`
	PublicBurst int     `yaml:"public_burst"`
	AdminRPS    float64 `yaml:"admin_rps"`
	AdminBurst  int     `yaml:"admin_burst"`
	LoginPerMin float64 `yaml:"login_per_min"` // 登录尝试次数/分钟（防爆破）
	RedisAddr   string  `yaml:"redis_addr"`    // 可选：配置后多实例共享限流窗口（按分钟固定窗口）
	RedisPass   string  `yaml:"redis_password"`
	RedisDB     int     `yaml:"redis_db"`
}

// SecurityCfg 安全相关配置。
type SecurityCfg struct {
	// TrustedProxies 可信反向代理 IP 列表（如 nginx 所在主机）。
	// 仅当直连对端在此列表中才解析 X-Forwarded-For；默认直连部署不信任 XFF。
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// RetentionCfg 数据保留策略（天数，0 = 永久保留）：
// 审计日志 / SQL 执行历史 / 配置版本快照按天清理，防止元库无限增长。
type RetentionCfg struct {
	AuditDays      int `yaml:"audit_days"`
	SQLHistoryDays int `yaml:"sql_history_days"`
	VersionDays    int `yaml:"version_days"`
	ChatDays       int `yaml:"chat_days"` // AI 对话历史
}

type Config struct {
	Server      Server       `yaml:"server"`
	Meta        Meta         `yaml:"meta"`
	DbPool      DbPool       `yaml:"dbpool"`
	Auth        AuthCfg      `yaml:"auth"`
	Query       QueryCfg     `yaml:"query"`
	RateLimit   RateLimitCfg `yaml:"ratelimit"`
	Security    SecurityCfg  `yaml:"security"`
	Retention   RetentionCfg `yaml:"retention"`
	Ai          AiCfg        `yaml:"ai"`
	Smtp        SmtpCfg      `yaml:"smtp"`
	Datasources []DS         `yaml:"datasources"`
}

// AuthEnabled 鉴权开关（未配置时默认开启）。
func (c *Config) AuthEnabled() bool { return c.Auth.Enabled == nil || *c.Auth.Enabled }

// Default 返回零配置可运行的默认配置：内置 sqlite 本地库。
func Default() *Config {
	c := &Config{
		Server: Server{Port: 8085},
		Meta:   Meta{Type: "sqlite", DSN: "./data/litereport.db"},
		Auth: AuthCfg{
			SessionHours: 24,
			Users:        []UserCfg{{Name: "admin", Password: "admin123", Role: "admin"}},
			APITokens:    []string{"demo-token-123"},
		},
		Query: QueryCfg{TimeoutSeconds: 30},
		RateLimit: RateLimitCfg{
			PublicRPS: 20, PublicBurst: 40,
			AdminRPS: 200, AdminBurst: 400,
			LoginPerMin: 10,
		},
		Datasources: []DS{
			{Key: "local", Name: "本地库", Type: "sqlite", DSN: "./data/litereport.db"},
		},
	}
	c.normalize()
	return c
}

// Load 读取 yaml 配置；文件不存在时使用默认配置。
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	c.normalize()
	return c, nil
}

func (c *Config) normalize() {
	if c.Server.Port == 0 {
		c.Server.Port = 8085
	}
	if c.Meta.Type == "" {
		c.Meta.Type = "sqlite"
	}
	if c.Meta.DSN == "" {
		c.Meta.DSN = "./data/litereport.db"
	}
	// 鉴权默认值
	if c.Auth.SessionHours <= 0 {
		c.Auth.SessionHours = 24
	}
	for i := range c.Auth.Users {
		r := strings.ToLower(strings.TrimSpace(c.Auth.Users[i].Role))
		if r != "admin" && r != "editor" && r != "viewer" {
			r = "admin"
		}
		c.Auth.Users[i].Role = r
	}
	if len(c.Auth.Users) == 0 {
		c.Auth.Users = []UserCfg{{Name: "admin", Password: "admin123", Role: "admin"}}
	}
	// 查询超时默认 30s
	if c.Query.TimeoutSeconds == 0 {
		c.Query.TimeoutSeconds = 30
	}
	// 限流默认值
	if c.RateLimit.PublicRPS <= 0 {
		c.RateLimit.PublicRPS = 20
	}
	if c.RateLimit.PublicBurst <= 0 {
		c.RateLimit.PublicBurst = 40
	}
	if c.RateLimit.AdminRPS <= 0 {
		c.RateLimit.AdminRPS = 200
	}
	if c.RateLimit.AdminBurst <= 0 {
		c.RateLimit.AdminBurst = 400
	}
	if c.RateLimit.LoginPerMin <= 0 {
		c.RateLimit.LoginPerMin = 10
	}
	// 数据保留默认值
	if c.Retention.AuditDays == 0 {
		c.Retention.AuditDays = 180
	}
	if c.Retention.SQLHistoryDays == 0 {
		c.Retention.SQLHistoryDays = 90
	}
	if c.Retention.VersionDays == 0 {
		c.Retention.VersionDays = 180
	}
	if c.Retention.ChatDays == 0 {
		c.Retention.ChatDays = 90
	}
	// 始终保证内置 local 数据源指向元数据库
	hasLocal := false
	for i := range c.Datasources {
		d := &c.Datasources[i]
		d.Type = strings.ToLower(strings.TrimSpace(d.Type))
		if d.Key == "local" {
			hasLocal = true
			if d.Name == "" {
				d.Name = "本地库"
			}
			if d.DSN == "" {
				d.Type, d.DSN = c.Meta.Type, c.Meta.DSN
			}
		}
	}
	if !hasLocal {
		c.Datasources = append([]DS{{
			Key: "local", Name: "本地库", Type: c.Meta.Type, DSN: c.Meta.DSN,
		}}, c.Datasources...)
	}
	if c.Meta.DSN == "./data/litereport.db" || strings.Contains(c.Meta.DSN, "/") || strings.Contains(c.Meta.DSN, "\\") {
		if dir := filepath.Dir(c.Meta.DSN); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
}

// AiCfg AI 生成 SQL 配置（OpenAI 兼容接口）。
type AiCfg struct {
	BaseURL string `yaml:"base_url"` // 如 https://api.openai.com/v1 或兼容网关
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// SmtpCfg 邮件服务配置（定时推送用）。
type SmtpCfg struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	From       string `yaml:"from"`
	AlertEmail string `yaml:"alert_email"` // 推送失败告警收件箱（空=不告警）
}
