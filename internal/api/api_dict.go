// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"net/http"
	"time"

	"litereport/internal/cache"

	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 字典 ─────

func (s *Server) handleDictList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.DictList(r.Context(), metaDB, dialect, r.URL.Query().Get("dictCode"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

func (s *Server) handleDictSave(w http.ResponseWriter, r *http.Request) {
	var d store.DictItem
	if err := readJSON(r, &d); err != nil || d.DictCode == "" || d.Label == "" {
		model.Err(w, "dictCode/label 不能为空")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	if err := store.DictSave(r.Context(), metaDB, dialect, d); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "字典保存", d.DictCode, d.Label+"="+d.Value)
	model.OK(w, nil)
}

func (s *Server) handleDictDelete(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	if err := store.DictDelete(r.Context(), metaDB, dialect, r.PathValue("id")); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "字典删除", r.PathValue("id"), "")
	model.OK(w, nil)
}

// dictCache 公共字典查询缓存。
var dictCache = cache.New()

// handleGetDict 公共接口：GET /online/cgreport/api/getDict/{code}
func (s *Server) handleGetDict(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if v, ok := dictCache.Get(code); ok {
		model.OK(w, v)
		return
	}
	metaDB, dialect := s.mgr.Meta()
	list, err := store.DictList(r.Context(), metaDB, dialect, code)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	items := make([]map[string]string, 0, len(list))
	for _, d := range list {
		items = append(items, map[string]string{"label": d.Label, "value": d.Value})
	}
	// 空结果不缓存：避免任意 code 枚举把无效键塞满缓存
	if len(items) == 0 {
		model.OK(w, items)
		return
	}
	dictCache.Set(code, items, 60*time.Second)
	model.OK(w, items)
}
