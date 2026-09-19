// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 配置版本 ─────

// snapshotPayload 报表配置快照。
type snapshotPayload struct {
	Head   *model.Head   `json:"head"`
	Items  []model.Item  `json:"items"`
	Params []model.Param `json:"params"`
}

// saveVersion 保存配置后落一份快照。
func (s *Server) saveVersion(r *http.Request, h *model.Head, items []model.Item, params []model.Param) {
	snap, err := json.Marshal(snapshotPayload{Head: h, Items: items, Params: params})
	if err != nil {
		return
	}
	metaDB, dialect := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = store.VersionInsert(ctx, metaDB, dialect, h.ID, string(snap), currentUser(r.Context()))
}

func (s *Server) handleVersionList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	key := r.URL.Query().Get("headId")
	list, err := store.VersionList(r.Context(), metaDB, dialect, key)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	// 兼容传报表编码：按 head_id 查不到时，解析成 head.id 再查一次
	if len(list) == 0 {
		if head, herr := store.HeadFindByKey(r.Context(), metaDB, dialect, key); herr == nil && head.ID != key {
			list, err = store.VersionList(r.Context(), metaDB, dialect, head.ID)
			if err != nil {
				model.Err(w, err.Error())
				return
			}
		}
	}
	model.OK(w, list)
}

func (s *Server) handleVersionGet(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	snap, err := store.VersionGet(r.Context(), metaDB, dialect, r.PathValue("vid"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, json.RawMessage(snap))
}

func (s *Server) handleVersionRollback(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	snap, err := store.VersionGet(r.Context(), metaDB, dialect, r.PathValue("vid"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	var p snapshotPayload
	if err := readJSONFromString(snap, &p); err != nil || p.Head == nil {
		model.Err(w, "版本快照解析失败")
		return
	}
	if err := store.HeadSave(r.Context(), metaDB, dialect, p.Head, p.Items, p.Params); err != nil {
		model.Err(w, "回滚失败: "+err.Error())
		return
	}
	s.invalidateCfgCache()
	s.audit(r, "配置回滚", p.Head.ReportCode, p.Head.ID)
	model.OK(w, p.Head)
}

func (s *Server) handleHeadExport(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	head, err := store.HeadGet(r.Context(), metaDB, dialect, r.PathValue("id"))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	items, _ := store.ItemsByHead(r.Context(), metaDB, dialect, head.ID)
	params, _ := store.ParamsByHead(r.Context(), metaDB, dialect, head.ID)
	snap, _ := json.MarshalIndent(snapshotPayload{Head: head, Items: items, Params: params}, "", "  ")
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+encodeRFC5987(head.ReportCode+".json"))
	_, _ = w.Write(snap)
}

func (s *Server) handleHeadImport(w http.ResponseWriter, r *http.Request) {
	var p snapshotPayload
	if err := readJSON(r, &p); err != nil || p.Head == nil || p.Head.ReportCode == "" {
		model.Err(w, "导入内容需为报表配置JSON（含 head/items/params）")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	// 编码冲突自动加后缀
	code := p.Head.ReportCode
	if _, err := store.HeadGetByCode(r.Context(), metaDB, dialect, code); err == nil {
		code = code + "_imp" + fmt.Sprintf("%04d", time.Now().UnixMilli()%10000)
		p.Head.ReportCode = code
	}
	p.Head.ID = store.NewID()
	if err := store.HeadSave(r.Context(), metaDB, dialect, p.Head, p.Items, p.Params); err != nil {
		model.Err(w, "导入失败: "+err.Error())
		return
	}
	s.audit(r, "配置导入", code, "")
	model.OK(w, p.Head)
}

// readJSONFromString 从字符串解析 JSON（UseNumber 语义同 readJSON）。
func readJSONFromString(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	return dec.Decode(v)
}
