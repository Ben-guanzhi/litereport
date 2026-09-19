// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"litereport/internal/auth"
	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 审计日志 ─────

// handleBackupExport 元库全量配置备份（admin）：下载 JSON。
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	data, err := store.BackupExport(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, "备份失败: "+err.Error())
		return
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "元库备份", "export", fmt.Sprintf("%dB", len(out)))
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=litereport-backup-"+time.Now().Format("20060102-150405")+".json")
	_, _ = w.Write(out)
}

// handleBackupRestore 元库配置恢复（admin）：上传 BackupExport 导出的 JSON，
// 整表替换（单事务；platform_user/platform_schema 不在恢复范围）。
// 请求体需含 confirm:true 双重确认，防误传。
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Product string                      `json:"product"`
		Confirm bool                        `json:"confirm"`
		Tables  map[string][]map[string]any `json:"tables"`
	}
	if err := readJSON(r, &body); err != nil || body.Product != "litereport" || len(body.Tables) == 0 {
		model.Err(w, "请上传 litereport 备份导出的 JSON（含 tables 字段）")
		return
	}
	if !body.Confirm {
		model.Err(w, "高风险操作：覆盖现有全部报表/表单/推送/数据源配置。请附带 confirm:true 再次提交")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	n, err := store.BackupRestore(r.Context(), metaDB, dialect, body.Tables)
	if err != nil {
		model.Err(w, "恢复失败(已回滚): "+err.Error())
		return
	}
	s.invalidateCfgCache()
	s.reloadDatasources()
	s.audit(r, "元库恢复", "restore", fmt.Sprintf("%d行", n))
	model.OK(w, map[string]any{"restoredRows": n})
}

// reloadDatasources 恢复配置后热加载：重建库内数据源连接池，移除已消失的页面数据源。
// 注意：恢复的 DSN 密文需与当前 auth.secret 匹配才能解密成功（解密失败的源状态为离线）。
func (s *Server) reloadDatasources() {
	metaDB, dialect := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := store.DSList(ctx, metaDB, dialect)
	if err != nil {
		slog.Error("恢复后加载数据源失败", "err", err)
		return
	}
	keys := map[string]bool{}
	for _, r := range rows {
		keys[r.Key] = true
		_ = s.mgr.Upsert(dbx.DSConfig{
			Key: r.Key, Name: r.Name, Type: r.Type,
			DSN: auth.DecryptDSN(r.DSN, s.auth.SecretString()), Source: "db",
		})
	}
	for _, v := range s.mgr.List() {
		if v.Source == "db" && !keys[v.Key] {
			s.mgr.Remove(v.Key)
		}
	}
}
