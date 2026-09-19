// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"net/http"

	"litereport/internal/model"
	"litereport/internal/service"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 图表 ─────

func (s *Server) handleChartList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.ChartList(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

func (s *Server) handleChartSave(w http.ResponseWriter, r *http.Request) {
	var c store.ChartRow
	if err := readJSON(r, &c); err != nil || c.ChartName == "" || c.ReportCode == "" || c.DimField == "" {
		model.Err(w, "图表名称/报表编码/维度字段不能为空")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	if err := store.ChartSave(r.Context(), metaDB, dialect, &c); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "图表保存", c.ChartName, c.ReportCode)
	model.OK(w, c)
}

func (s *Server) handleChartDelete(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	if err := store.ChartDelete(r.Context(), metaDB, dialect, r.PathValue("id")); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "图表删除", r.PathValue("id"), "")
	model.OK(w, nil)
}

// handleChartData 公共接口：GET /online/cgreport/api/getChartData/{code}?dim=&measure=&agg=&limit=
// 结果带 30s TTL 缓存（大屏多图表并发刷新友好）。
func (s *Server) handleChartData(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	q := r.URL.Query()
	ck := service.ChartCacheKey(code, q.Get("dim"), q.Get("measure"), q.Get("agg"), atoi(q.Get("limit")), collectFixedParams(q))
	if pts, ok := service.ChartCacheGet(ck); ok {
		model.OK(w, map[string]any{
			"chart":  map[string]string{"name": pack.head.ReportName},
			"points": pts, "cached": true,
		})
		return
	}
	points, err := service.ChartData(r.Context(), target, dialect, pack.head, pack.items, pack.params, q,
		q.Get("dim"), q.Get("measure"), q.Get("agg"), atoi(q.Get("limit")))
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	service.ChartCacheSet(ck, points)
	model.OK(w, map[string]any{
		"chart":  map[string]string{"name": pack.head.ReportName},
		"points": points,
	})
}
