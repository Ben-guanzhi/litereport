// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"fmt"
	"net/http"
	"strings"

	"litereport/internal/auth"

	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 数据源管理 ─────

func (s *Server) handleDSListFull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	metaDB, dialect := s.mgr.Meta()
	rows, err := store.DSList(ctx, metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	// 合并视图：库内数据源优先（覆盖同名 config 项），状态取一次 List（Manager 侧带 TTL 缓存）
	listView := map[string]dbx.DSView{}
	for _, v := range s.mgr.List() {
		listView[v.Key] = v
	}
	byKey := map[string]map[string]any{}
	for _, row := range rows {
		status := "unknown"
		if v, ok := listView[row.Key]; ok {
			status = v.Status
		}
		byKey[row.Key] = map[string]any{
			"key": row.Key, "name": row.Name, "type": row.Type, "status": status,
			"source": "db", "dsnMasked": maskDSN(row.DSN),
		}
	}
	for _, v := range listView {
		if _, ok := byKey[v.Key]; !ok {
			byKey[v.Key] = map[string]any{"key": v.Key, "name": v.Name, "type": v.Type, "status": v.Status, "source": "config"}
		}
	}
	out := make([]map[string]any, 0, len(byKey))
	for _, m := range byKey {
		out = append(out, m)
	}
	model.OK(w, out)
}

func maskDSN(dsn string) string {
	if strings.HasPrefix(dsn, "enc1:") {
		return "（已加密存储）"
	}
	// 简单掩码：隐藏 :password@ 段
	if i := strings.Index(dsn, "://"); i > 0 {
		rest := dsn[i+3:]
		if at := strings.Index(rest, "@"); at > 0 {
			head := rest[:at]
			if c := strings.Index(head, ":"); c > 0 {
				return dsn[:i+3] + head[:c] + ":***" + rest[at:]
			}
		}
	}
	if i := strings.Index(dsn, "password="); i >= 0 {
		return dsn[:i] + "password=***"
	}
	return dsn
}

func (s *Server) handleDSSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key, Name, DType, DSN string
	}
	var raw struct {
		Key     string `json:"key"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		DSN     string `json:"dsn"`
		KeepDsn bool   `json:"keepDsn"`
		DsnEnc  bool   `json:"dsnEnc"` // true 表示 DSN 已加密（编辑时未修改，直接透传密文）
	}
	if err := readJSON(r, &raw); err != nil {
		model.Err(w, "请求体解析失败")
		return
	}
	body.Key, body.Name, body.DType, body.DSN = raw.Key, raw.Name, raw.Type, raw.DSN
	if body.Key == "" || body.Name == "" || body.DType == "" {
		model.Err(w, "key/名称/类型不能为空")
		return
	}
	// DSN 加密落库；keepDsn 表示编辑时未修改（沿用原密文）
	stored := body.DSN
	plainDSN := body.DSN
	if raw.KeepDsn {
		metaDB0, dialect0 := s.mgr.Meta()
		if rows0, err0 := store.DSList(r.Context(), metaDB0, dialect0); err0 == nil {
			for _, r0 := range rows0 {
				if r0.Key == body.Key {
					stored = r0.DSN
					plainDSN = auth.DecryptDSN(stored, s.auth.SecretString())
				}
			}
		}
	} else {
		stored = auth.EncryptDSN(body.DSN, s.auth.SecretString())
	}
	row := store.DSRow{Key: body.Key, Name: body.Name, Type: dbx.Normalize(body.DType), DSN: stored}
	if err := s.mgr.Upsert(dbx.DSConfig{Key: row.Key, Name: row.Name, Type: row.Type, DSN: plainDSN, Source: "db"}); err != nil {
		model.Err(w, err.Error())
		return
	}
	metaDB, dialect := s.mgr.Meta()
	if err := store.DSSave(r.Context(), metaDB, dialect, row); err != nil {
		model.Err(w, "保存失败: "+err.Error())
		return
	}
	s.audit(r, "数据源保存", row.Key, row.Type)
	model.OK(w, nil)
}

func (s *Server) handleDSTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"`
		DSN  string `json:"dsn"`
	}
	if err := readJSON(r, &body); err != nil {
		model.Err(w, "请求体解析失败")
		return
	}
	if err := dbx.TestConnection(body.Type, body.DSN); err != nil {
		model.Err(w, "连接失败: "+err.Error())
		return
	}
	model.OK(w, map[string]any{"ok": true})
}

func (s *Server) handleDSDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	metaDB, dialect := s.mgr.Meta()
	// 引用检查：仍有报表使用该数据源时拒绝删除
	codes, err := store.HeadCodesBySource(r.Context(), metaDB, dialect, key)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	if len(codes) > 0 {
		shown := codes
		if len(shown) > 5 {
			shown = shown[:5]
		}
		model.Err(w, fmt.Sprintf("数据源[%s]仍被 %d 个报表引用（%s%s），请先调整这些报表的数据源", key, len(codes), strings.Join(shown, ", "), map[bool]string{true: " …", false: ""}[len(codes) > 5]))
		return
	}
	if err := store.DSDelete(r.Context(), metaDB, dialect, key); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.mgr.Remove(key)
	s.audit(r, "数据源删除", key, "")
	model.OK(w, nil)
}
