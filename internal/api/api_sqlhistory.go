// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"context"
	"net/http"
	"time"

	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── SQL 执行历史 ─────

// handleSQLHistory GET /api/sqlhistory?user=&pageNo=&pageSize=
func (s *Server) handleSQLHistory(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	q := r.URL.Query()
	page, size := atoi(q.Get("pageNo")), atoi(q.Get("pageSize"))
	user := q.Get("user")
	if user == "" && currentUserRole(r.Context()) != "admin" {
		user = currentUser(r.Context()) // 编辑只能看自己的
	}
	list, total, err := store.SQLHistoryList(r.Context(), metaDB, dialect, user, page, size)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	model.OK(w, model.Page{Records: list, Total: total, Size: size, Current: page, Pages: (total + size - 1) / size})
}

// recordSQLHistory 异步记录一条 SQL 执行。
func (s *Server) recordSQLHistory(r *http.Request, dbSource, sqlText string, ms int64, rows int, execErr error) {
	user := currentUser(r.Context())
	if user == "" {
		user = "anonymous"
	}
	h := store.SQLHistoryRow{
		UserName: user, DbSource: dbSource, SQLText: sqlText,
		DurationMS: ms, RowCount: rows, OK: "Y",
	}
	if execErr != nil {
		h.OK = "N"
		msg := execErr.Error()
		if len(msg) > 480 {
			msg = msg[:480]
		}
		h.ErrMsg = msg
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		metaDB, dialect := s.mgr.Meta()
		_ = store.SQLHistoryInsert(ctx, metaDB, dialect, h)
	}()
}
