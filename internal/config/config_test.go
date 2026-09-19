package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultConfig 零配置默认值。
func TestDefaultConfig(t *testing.T) {
	c := Default()
	if c.Server.Port != 8085 {
		t.Errorf("默认端口 = %d", c.Server.Port)
	}
	if !c.AuthEnabled() {
		t.Error("鉴权默认应开启")
	}
	if c.Auth.SessionHours != 24 {
		t.Errorf("session_hours = %d", c.Auth.SessionHours)
	}
	if c.Retention.AuditDays != 180 || c.Retention.SQLHistoryDays != 90 || c.Retention.VersionDays != 180 {
		t.Errorf("保留策略默认值异常: %+v", c.Retention)
	}
}

// TestNormalizeConfig yaml 加载后的归一化：角色、local 数据源、默认值。
func TestNormalizeConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  port: 9000
meta:
  type: mysql
  dsn: "root:x@tcp(127.0.0.1:3306)/litereport"
auth:
  enabled: false
  users:
    - name: boss
      password: secret1
      role: SUPER      # 非法角色应归一为 admin
datasources:
  - key: pg1
    name: pg
    type: postgresql
    dsn: "host=127.0.0.1 dbname=x"
ratelimit:
  public_rps: 0        # 应回默认
retention:
  audit_days: 0
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 9000 {
		t.Errorf("port = %d", c.Server.Port)
	}
	if c.AuthEnabled() {
		t.Error("enabled:false 应关闭鉴权")
	}
	if c.Auth.Users[0].Role != "admin" {
		t.Errorf("非法角色应归一为admin, got %q", c.Auth.Users[0].Role)
	}
	// local 数据源自动补全并指向元库
	hasLocal := false
	for _, d := range c.Datasources {
		if d.Key == "local" {
			hasLocal = true
			if d.Type != "mysql" {
				t.Errorf("local 应指向元库类型 mysql, got %q", d.Type)
			}
		}
	}
	if !hasLocal {
		t.Error("应自动补全 local 数据源")
	}
	if c.RateLimit.PublicRPS != 20 {
		t.Errorf("public_rps 应回默认20, got %v", c.RateLimit.PublicRPS)
	}
	if c.Retention.AuditDays != 180 {
		t.Errorf("audit_days 应回默认180, got %d", c.Retention.AuditDays)
	}
}

// TestLoadMissingFile 配置文件不存在时使用默认配置。
func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 8085 {
		t.Errorf("缺失文件应走默认配置, port = %d", c.Server.Port)
	}
}
