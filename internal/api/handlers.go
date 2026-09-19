package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"litereport/internal/metrics"
	"litereport/internal/model"
	"litereport/internal/service"
	"litereport/internal/store"
)

// handleGetData 公共查询接口：GET /online/cgreport/api/getData/{code}
// 支持 pageNo/pageSize、column/order 排序、字段配置的查询条件、${param} 报表参数。
func (s *Server) handleGetData(w http.ResponseWriter, r *http.Request) {
	s.queryData(w, r, true)
}

// handleGetDataNoPage 公共查询接口（不分页）：GET /online/cgreport/api/getDataNoPage/{code}
func (s *Server) handleGetDataNoPage(w http.ResponseWriter, r *http.Request) {
	s.queryData(w, r, false)
}

func (s *Server) queryData(w http.ResponseWriter, r *http.Request, paged bool) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	q := r.URL.Query()
	if !paged {
		q.Set("pageNo", "1")
		q.Set("pageSize", "100000")
		q.Set("needCount", "false") // 不分页查询跳过 COUNT（total 返回 -1）
	}
	// 行级数据权限：非管理员且报表启用时，注入 {user} 渲染后的过滤条件
	q.Del("__perm")
	user := currentUser(r.Context())
	if user != "" && user != "admin" && strings.EqualFold(pack.head.PermEnabled, "Y") && pack.head.PermFilter != "" {
		q.Set("__perm", strings.ReplaceAll(pack.head.PermFilter, "{user}", user))
	}
	res, err := service.QueryReport(r.Context(), target, dialect, pack.head, pack.items, pack.params, q)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	// 慢查询台账：超过 3s 记入审计
	if res.ElapsedMS > 3000 {
		s.audit(r, "慢查询", code, fmt.Sprintf("%dms", res.ElapsedMS))
	}
	metrics.ObserveQuery(code, float64(res.ElapsedMS))
	size := 10
	page := 1
	if v := q.Get("pageSize"); v != "" {
		if n := atoi(v); n > 0 {
			size = n
		}
	}
	if v := q.Get("pageNo"); v != "" {
		if n := atoi(v); n > 0 {
			page = n
		}
	}
	pages := -1
	if res.Total >= 0 {
		pages = (res.Total + size - 1) / size
	}
	model.OK(w, model.Page{Records: res.Records, Total: res.Total, Size: size, Current: page, Pages: pages, Summary: res.Summary})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}

// handleGetColumns 公共接口：GET /online/cgreport/api/getColumns/{code}
// 返回字段配置与查询参数配置，用于渲染报表。
func (s *Server) handleGetColumns(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	_, _, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	model.OK(w, map[string]any{
		"head":    pack.head,
		"fields":  pack.items,
		"params":  pack.params,
		"columns": columnNames(pack.items),
	})
}

func columnNames(items []model.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.FieldName)
	}
	return out
}

// handleGetInfo 公共接口：GET /online/cgreport/api/getInfo/{codeOrId}
func (s *Server) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	_, _, pack, err := s.loadReport(r.Context(), key)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	h := *pack.head
	model.OK(w, map[string]any{
		"head":    h,
		"fields":  pack.items,
		"params":  pack.params,
		"columns": columnNames(pack.items),
	})
}

// handleSaveData 公共保存接口：POST /online/cgreport/api/saveData/{code}
// 请求体：{"data":{...}} 单条 或 {"dataList":[{...},{...}]} 批量，可选 "table" 指定表名。
// data.id 有值且存在则更新，否则插入（id 为空自动生成）。
func (s *Server) handleSaveData(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	_, _, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	var req service.SaveRequest
	if err := readJSON(r, &req); err != nil {
		model.Err(w, "请求体JSON解析失败: "+err.Error())
		return
	}
	target, dialect, err := s.openTarget(pack.head.DbSource)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	saved, ids, err := service.SaveData(r.Context(), target, dialect, pack.head, req)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	service.InvalidateCounts()
	s.audit(r, "数据写入", code, "")
	model.OK(w, map[string]any{"saved": saved, "ids": ids})
}

// handleDeleteData 公共删除接口：POST/DELETE /online/cgreport/api/deleteData/{code}
// 请求体 {"ids":[".."]} 或 query ?id=a,b
func (s *Server) handleDeleteData(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	_, _, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	var ids []string
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = readJSON(r, &body)
	ids = append(ids, body.IDs...)
	if v := r.URL.Query().Get("id"); v != "" {
		ids = append(ids, splitComma(v)...)
	}
	target, dialect, err := s.openTarget(pack.head.DbSource)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	n, err := service.DeleteData(r.Context(), target, dialect, pack.head, ids)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	service.InvalidateCounts()
	s.audit(r, "数据删除", code, "")
	model.OK(w, map[string]any{"deleted": n})
}

func (s *Server) openTarget(key string) (*sql.DB, string, error) {
	return s.mgr.Get(key)
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ───────────────────────── 管理接口 ─────────────────────────

// handleHeadList GET /online/cgreport/head/list?reportName=&reportCode=&pageNo=&pageSize=
func (s *Server) handleHeadList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	page := atoi(r.URL.Query().Get("pageNo"))
	size := atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	list, total, err := store.HeadList(r.Context(), metaDB, dialect,
		r.URL.Query().Get("reportName"), r.URL.Query().Get("reportCode"), r.URL.Query().Get("category"), page, size)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	model.OK(w, model.Page{Records: list, Total: total, Size: size, Current: page, Pages: (total + size - 1) / size})
}

// handleHeadGet GET /online/cgreport/head/{id}
func (s *Server) handleHeadGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	metaDB, dialect := s.mgr.Meta()
	head, err := store.HeadGet(r.Context(), metaDB, dialect, id)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	items, _ := store.ItemsByHead(r.Context(), metaDB, dialect, head.ID)
	params, _ := store.ParamsByHead(r.Context(), metaDB, dialect, head.ID)
	model.OK(w, map[string]any{"head": head, "items": items, "params": params})
}

type savePayload struct {
	ID          string        `json:"id"`
	ReportName  string        `json:"reportName"`
	ReportCode  string        `json:"reportCode"`
	CgSQL       string        `json:"cgSql"`
	DbSource    string        `json:"dbSource"`
	Category    string        `json:"category"`
	PermFilter  string        `json:"permFilter"`
	PermEnabled string        `json:"permEnabled"`
	Items       []model.Item  `json:"items"`
	Params      []model.Param `json:"params"`
}

// handleHeadSave POST/PUT /online/cgreport/head
func (s *Server) handleHeadSave(w http.ResponseWriter, r *http.Request) {
	createUser := currentUser(r.Context())
	if createUser == "" {
		createUser = "admin"
	}
	var p savePayload
	if err := readJSON(r, &p); err != nil {
		model.Err(w, "请求体JSON解析失败: "+err.Error())
		return
	}
	if p.ReportName == "" || p.ReportCode == "" {
		model.Err(w, "报表编码与报表名字不能为空")
		return
	}
	sqlText, err := service.CleanSQL(p.CgSQL)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	metaDB, dialect := s.mgr.Meta()
	h := &model.Head{
		ID: p.ID, ReportName: p.ReportName, ReportCode: p.ReportCode,
		CgSQL: sqlText, DbSource: p.DbSource, CreateBy: createUser, UpdateBy: createUser,
		Category: p.Category, PermFilter: p.PermFilter, PermEnabled: p.PermEnabled,
	}
	if h.ID == "" {
		h.ID = store.NewID()
		// 编码唯一性
		if _, err := store.HeadGetByCode(r.Context(), metaDB, dialect, h.ReportCode); err == nil {
			model.Err(w, "报表编码["+h.ReportCode+"]已存在")
			return
		}
	} else {
		old, err := store.HeadGet(r.Context(), metaDB, dialect, h.ID)
		if err != nil {
			model.Err(w, s.errOf(err))
			return
		}
		if dup, err := store.HeadGetByCode(r.Context(), metaDB, dialect, h.ReportCode); err == nil && dup.ID != h.ID {
			model.Err(w, "报表编码["+h.ReportCode+"]已被其他报表使用")
			return
		}
		if h.DbSource == "" {
			h.DbSource = old.DbSource
		}
		if h.Category == "" {
			h.Category = old.Category
		}
	}
	items := p.Items
	for i := range items {
		items[i].OrderNum = i + 1
		if items[i].IsShow == "" {
			items[i].IsShow = "Y"
		}
		if items[i].IsQuery == "" {
			items[i].IsQuery = "N"
		}
		if items[i].QueryMode == "" {
			items[i].QueryMode = "="
		}
		if items[i].IsTotal == "" {
			items[i].IsTotal = "N"
		}
	}
	if err := store.HeadSave(r.Context(), metaDB, dialect, h, items, p.Params); err != nil {
		model.Err(w, "保存失败: "+err.Error())
		return
	}
	s.invalidateCfgCache()
	s.saveVersion(r, h, items, p.Params)
	s.audit(r, "报表保存", h.ReportCode, h.ReportName)
	model.OK(w, h)
}

// handleHeadDelete DELETE /online/cgreport/head/{id}
func (s *Server) handleHeadDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	metaDB, dialect := s.mgr.Meta()
	if err := store.HeadDelete(r.Context(), metaDB, dialect, id); err != nil {
		model.Err(w, "删除失败: "+err.Error())
		return
	}
	s.invalidateCfgCache()
	s.audit(r, "报表删除", id, "")
	model.OK(w, nil)
}

// handleSQLParse POST /online/cgreport/sql/parse  body {"sql":"...","dbSource":"local"}
func (s *Server) handleSQLParse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SQL      string `json:"sql"`
		DbSource string `json:"dbSource"`
	}
	if err := readJSON(r, &body); err != nil {
		model.Err(w, "请求体JSON解析失败: "+err.Error())
		return
	}
	target, dialect, err := s.mgr.Get(body.DbSource)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	fields, err := service.ParseSQL(r.Context(), target, dialect, body.SQL)
	s.recordSQLHistory(r, body.DbSource, body.SQL, 0, len(fields), err)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	model.OK(w, map[string]any{"fields": fields})
}

// handleDatasourceList GET /api/datasource/list
func (s *Server) handleDatasourceList(w http.ResponseWriter, r *http.Request) {
	model.OK(w, s.mgr.List())
}

// errOf 把底层错误翻译为用户可读信息（超时单独提示）。
// 数据源连接类错误可能携带 DSN/主机/路径细节，统一脱敏——原始错误进服务端日志。
func (s *Server) errOf(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("查询超时（超过 %s），请优化报表SQL或调整 query.timeout_seconds 配置", s.queryTimeout)
	}
	msg := err.Error()
	if strings.Contains(msg, "连接数据源") || strings.Contains(msg, "打开数据源") {
		slog.Error("数据源连接失败", "err", msg)
		return "数据源暂不可用，请检查数据源配置或联系管理员"
	}
	return msg
}
