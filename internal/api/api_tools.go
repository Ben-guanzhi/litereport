// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"litereport/internal/model"
	"litereport/internal/service"
)

// 本文件由 extra.go 按域拆分生成。

func (s *Server) registerExtraRoutes2() {
	// 交叉表（公共）
	s.mux.HandleFunc("GET /online/cgreport/api/getCrossTable/{code}", s.handleCrossTable)
	// SQL 试运行（管理端）
	s.mux.HandleFunc("POST /online/cgreport/sql/preview", s.requireRole(s.handleSQLPreview, "editor"))
	// 表浏览器（管理端）
	s.mux.HandleFunc("GET /api/datasource/tables", s.requireRole(s.handleDSTables, "editor"))
	s.mux.HandleFunc("GET /api/datasource/columns", s.requireRole(s.handleDSColumns, "editor"))
}

// handleCrossTable 公共接口：GET /online/cgreport/api/getCrossTable/{code}?row=&col=&measure=&agg=
func (s *Server) handleCrossTable(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	q := r.URL.Query()
	ct, err := service.CrossTableData(r.Context(), target, dialect, pack.head, pack.items, pack.params, q,
		q.Get("row"), q.Get("col"), q.Get("measure"), q.Get("agg"), atoi(q.Get("maxCols")))
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	model.OK(w, ct)
}

// handleSQLPreview POST /online/cgreport/sql/preview {sql, dbSource, confirmed}
// SQL 命中危险特征时返回 needConfirm=true（前端弹确认框后带 confirmed=true 重发）。
func (s *Server) handleSQLPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SQL       string `json:"sql"`
		DbSource  string `json:"dbSource"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := readJSON(r, &body); err != nil || body.SQL == "" {
		model.Err(w, "请提供报表SQL")
		return
	}
	if reasons := service.CheckDangerousSQL(body.SQL); len(reasons) > 0 && !body.Confirmed {
		model.OK(w, map[string]any{"needConfirm": true, "reasons": reasons})
		return
	}
	target, dialect, err := s.mgr.Get(body.DbSource)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	started := time.Now()
	cols, records, err := service.PreviewSQL(r.Context(), target, dialect, body.SQL, 10)
	s.recordSQLHistory(r, body.DbSource, body.SQL, time.Since(started).Milliseconds(), len(records), err)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	model.OK(w, map[string]any{"columns": cols, "records": records})
}

// handleDSTables GET /api/datasource/tables?key=local
func (s *Server) handleDSTables(w http.ResponseWriter, r *http.Request) {
	target, dialect, err := s.mgr.Get(r.URL.Query().Get("key"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	tables, err := service.ListTables(r.Context(), target, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, tables)
}

// handleDSColumns GET /api/datasource/columns?key=&table=
func (s *Server) handleDSColumns(w http.ResponseWriter, r *http.Request) {
	target, dialect, err := s.mgr.Get(r.URL.Query().Get("key"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	cols, err := service.ListTableColumns(r.Context(), target, dialect, r.URL.Query().Get("table"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, cols)
}

// collectFixedParams 提取影响数据结果的查询参数（排除分页/排序/控制项/凭证）用于缓存键。
func collectFixedParams(q url.Values) string {
	var keys []string
	for k := range q {
		switch k {
		case "pageNo", "pageSize", "column", "order", "needCount", "needSummary", "limit", "row", "col", "maxCols",
			"token", "_" /* 凭证与时间戳不入键：token 避免泄入缓存，_ 防缓存穿透的随机参数无数据影响 */ :
			continue
		}
		keys = append(keys, k+"="+q.Get(k))
	}
	sort.Strings(keys)
	return strings.Join(keys, "&")
}
