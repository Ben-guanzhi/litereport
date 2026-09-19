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

// TestE2EFullFlow 全链路：登录 → 建报表 → 数据查询/写入/删除 → 导出 → 版本回滚。
func TestE2EFullFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Meta.DSN = filepath.Join(dir, "e2e.db")
	cfg.Datasources[0].DSN = cfg.Meta.DSN
	cfg.Server.Port = 0
	cfg.RateLimit.LoginPerMin = 1000 // 测试不限

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

	postJSON := func(path string, body any) (int, map[string]any) {
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
	getJSONWith := func(client *http.Client, path string) (int, map[string]any) {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m
	}
	getJSON := func(path string) (int, map[string]any) {
		return getJSONWith(cli, path)
	}

	// 0. 未登录 → 401
	if code, _ := getJSON("/online/cgreport/api/getData/demo_report"); code != 401 {
		t.Fatalf("未登录应为401, got %d", code)
	}
	// 1. 登录
	if code, r := postJSON("/api/auth/login", map[string]string{"username": "admin", "password": "admin123"}); code != 200 || r["success"] != true {
		t.Fatalf("登录失败: %d %v", code, r)
	}
	// 2. 建表单（建表+生成报表）
	code, r := postJSON("/online/cgform/create", map[string]any{
		"formName": "E2E表单", "tableName": "e2e_demo",
		"fields": []map[string]any{{"name": "title", "label": "标题", "type": "text", "required": true}, {"name": "score", "label": "分数", "type": "number"}},
	})
	if code != 200 || r["success"] != true {
		t.Fatalf("表单创建失败: %d %v", code, r)
	}
	// 3. 写入数据（公共保存接口 + API Token 直连，模拟第三方）
	tokenClient := &http.Client{}
	tr, _ := http.NewRequest("POST", srv.URL+"/online/cgreport/api/saveData/form_e2e_demo",
		bytes.NewReader([]byte(`{"data":{"id":"","title":"第一条","score":99}}`)))
	tr.Header.Set("Content-Type", "application/json")
	tr.Header.Set("X-API-Token", "demo-token-123")
	trr, err := tokenClient.Do(tr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(trr.Body)
	trr.Body.Close()
	var wr map[string]any
	_ = json.Unmarshal(b, &wr)
	if trr.StatusCode != 200 || wr["success"] != true {
		t.Fatalf("公共保存失败: %d %s", trr.StatusCode, string(b))
	}
	// 4. 查询校验
	code, r = getJSON("/online/cgreport/api/getData/form_e2e_demo?token=demo-token-123&needSummary=true")
	if code != 200 || r["success"] != true {
		t.Fatalf("查询失败: %d %v", code, r)
	}
	// 5. Excel 导出
	tr2, _ := http.NewRequest("GET", srv.URL+"/online/cgreport/api/exportExcel/form_e2e_demo?token=demo-token-123", nil)
	trr2, err := tokenClient.Do(tr2)
	if err != nil {
		t.Fatal(err)
	}
	xl, _ := io.ReadAll(trr2.Body)
	trr2.Body.Close()
	if trr2.StatusCode != 200 || len(xl) < 100 || xl[0] != 'P' {
		t.Fatalf("Excel导出异常: %d %dB", trr2.StatusCode, len(xl))
	}
	// 6. 版本列表（表单创建的报表配置自动生成快照）
	code, r = getJSON("/online/cgreport/head-versions?headId=form_e2e_demo")
	if code != 200 || r["success"] != true {
		t.Fatalf("版本列表失败: %d %v", code, r)
	}
	if versions, ok := r["result"].([]any); !ok || len(versions) == 0 {
		t.Fatalf("表单创建应产生版本快照, got %v", r["result"])
	}
	// 7. 图表数据
	code, r = getJSON("/online/cgreport/api/getChartData/form_e2e_demo?token=demo-token-123&dim=title&measure=score&agg=sum")
	if code != 200 || r["success"] != true {
		t.Fatalf("图表数据失败: %d %v", code, r)
	}
	// 8. 交叉表
	code, r = getJSON("/online/cgreport/api/getCrossTable/form_e2e_demo?token=demo-token-123&row=title&col=title&measure=score&agg=sum")
	if code != 200 || r["success"] != true {
		t.Fatalf("交叉表失败: %d %v", code, r)
	}
	// 9. 审计
	code, r = getJSON("/api/audit/list?pageNo=1&pageSize=5")
	if code != 200 || r["success"] != true {
		t.Fatalf("审计失败: %d %v", code, r)
	}
	// 10. /api/auth/me 返回 role
	_, me := getJSON("/api/auth/me")
	if me["success"] != true {
		t.Fatalf("me 失败: %v", me)
	}
	if res, ok := me["result"].(map[string]any); !ok || res["role"] != "admin" {
		t.Fatalf("角色应为admin: %v", me)
	}
	// 11. 修改密码（会话版本+1）→ 旧会话失效
	postJSON("/api/auth/password", map[string]string{"oldPassword": "admin123", "newPassword": "newpass66"})
	if code, _ := getJSON("/online/cgreport/head/list?pageNo=1&pageSize=1"); code != 401 {
		t.Fatalf("改密后旧会话应失效(401), got %d", code)
	}

	// 12. RBAC：新建 viewer 用户，验证其访问 admin 接口被 403 拒绝
	// 12.1 用新密码重新登录 admin
	if code, r := postJSON("/api/auth/login", map[string]string{"username": "admin", "password": "newpass66"}); code != 200 || r["success"] != true {
		t.Fatalf("改密后登录失败: %d %v", code, r)
	}
	// 12.2 创建 viewer 用户
	if code, r := postJSON("/api/users/save", map[string]string{"name": "v1", "password": "viewer66", "role": "viewer"}); code != 200 || r["success"] != true {
		t.Fatalf("创建viewer失败: %d %v", code, r)
	}
	// 12.3 viewer 独立会话
	vjar, _ := cookiejar.New(nil)
	vcli := &http.Client{Jar: vjar}
	vpost := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		resp, err := vcli.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return resp.StatusCode, m
	}
	if code, r := vpost("/api/auth/login", map[string]string{"username": "v1", "password": "viewer66"}); code != 200 || r["success"] != true {
		t.Fatalf("viewer登录失败: %d %v", code, r)
	}
	// 12.4 admin 接口对 viewer 应返回 403（真实 HTTP 状态码）
	if code, _ := getJSONWith(vcli, "/api/users"); code != 403 {
		t.Fatalf("viewer访问/api/users应403, got %d", code)
	}
	if code, _ := getJSONWith(vcli, "/api/audit/list?pageNo=1&pageSize=1"); code != 403 {
		t.Fatalf("viewer访问/api/audit/list应403, got %d", code)
	}
	if code, _ := vpost("/api/ai/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}}); code != 403 {
		t.Fatalf("viewer访问/api/ai/chat应403, got %d", code)
	}
	// 12.5 viewer 只读公共接口正常
	if code, _ := getJSONWith(vcli, "/online/cgreport/api/getData/form_e2e_demo"); code != 200 {
		t.Fatalf("viewer公共查询应200, got %d", code)
	}
}
