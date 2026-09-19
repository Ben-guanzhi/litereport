package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"litereport/internal/config"
	"litereport/internal/dbx"
	"litereport/internal/service"
)

// TestUserManagementGuard 用户管理保护：config 用户不可遮蔽、不可删除自己。
func TestUserManagementGuard(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Meta.DSN = filepath.Join(dir, "test.db")
	cfg.Datasources[0].DSN = cfg.Meta.DSN
	cfg.Server.Port = 0
	cfg.RateLimit.LoginPerMin = 1000

	metaDB, err := dbx.Open(cfg.Meta.Type, cfg.Meta.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer metaDB.Close()
	mgr := dbx.NewManagerWithPool(metaDB, "sqlite", []dbx.DSConfig{
		{Key: "local", Name: "本地库", Type: "sqlite", DSN: cfg.Meta.DSN},
	}, dbx.PoolSpec{})
	if err := service.Bootstrap(mgr); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewServer(cfg, mgr, "../../web").Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		resp, err := cli.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m
	}
	del := func(path string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		resp, err := cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m
	}

	if code, r := post("/api/auth/login", map[string]string{"username": "admin", "password": "admin123"}); code != 200 || r["success"] != true {
		t.Fatalf("登录失败: %d %v", code, r)
	}

	// 1. 页面新建/修改 config 用户(admin)应被拒绝
	code, r := post("/api/users/save", map[string]string{"name": "admin", "password": "xxx666", "role": "viewer"})
	if code != 200 || r["success"] != false {
		t.Fatalf("遮蔽 config 用户应被拒绝: %d %v", code, r)
	}
	// 2. 删除自己应被拒绝
	code, r = del("/api/users/admin")
	if code != 200 || r["success"] != false {
		t.Fatalf("删除自己应被拒绝: %d %v", code, r)
	}
	// 3. 正常新建 DB 用户仍可用
	code, r = post("/api/users/save", map[string]string{"name": "dba1", "password": "dba123", "role": "admin"})
	if code != 200 || r["success"] != true {
		t.Fatalf("新建用户失败: %d %v", code, r)
	}
}
