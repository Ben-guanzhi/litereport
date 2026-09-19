// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"litereport/internal/model"
	"litereport/internal/service"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 表单开发 ─────

// handleFormList GET /online/cgform/list
func (s *Server) handleFormList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.FormList(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

// handleFormCreate POST /online/cgform/create：建表 + 自动生成报表配置。
func (s *Server) handleFormCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FormName  string              `json:"formName"`
		TableName string              `json:"tableName"`
		Fields    []service.FormField `json:"fields"`
	}
	if err := readJSON(r, &body); err != nil || body.FormName == "" || body.TableName == "" || len(body.Fields) == 0 {
		model.Err(w, "表单名称/表名/字段列表不能为空")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	if err := service.CreateFormTable(r.Context(), metaDB, dialect, body.TableName, body.Fields); err != nil {
		model.Err(w, err.Error())
		return
	}
	// 生成表单记录
	fj, _ := json.Marshal(body.Fields)
	f := &store.FormRow{FormName: body.FormName, TableName: body.TableName, FieldsJSON: string(fj), CreateBy: currentUser(r.Context())}
	if err := store.FormSave(r.Context(), metaDB, dialect, f); err != nil {
		model.Err(w, err.Error())
		return
	}
	// 自动创建报表配置（数据维护页复用 AUTO在线报表）
	code := "form_" + strings.ToLower(body.TableName)
	if _, err := store.HeadGetByCode(r.Context(), metaDB, dialect, code); err == nil {
		model.OK(w, map[string]any{"reportCode": code, "existed": true})
		return
	}
	fields, err := service.ParseSQL(r.Context(), metaDB, dialect, "select * from "+body.TableName)
	if err != nil {
		model.Err(w, "字段解析失败: "+err.Error())
		return
	}
	head := &model.Head{
		ID: store.NewID(), ReportName: body.FormName, ReportCode: code,
		CgSQL: "select * from " + body.TableName, DbSource: "local", CreateBy: currentUser(r.Context()),
	}
	items := make([]model.Item, 0, len(fields))
	for i, fd := range fields {
		items = append(items, model.Item{
			FieldName: fd.Name, FieldTxt: fd.Text, FieldType: fd.Type,
			IsShow: "Y", IsQuery: "N", QueryMode: "=", OrderNum: i + 1,
		})
	}
	if err := store.HeadSave(r.Context(), metaDB, dialect, head, items, nil); err != nil {
		model.Err(w, "报表配置生成失败: "+err.Error())
		return
	}
	// 表单生成的报表配置同样记入版本快照（与其他保存路径一致，支持回滚）
	if snap, err := json.MarshalIndent(snapshotPayload{Head: head, Items: items}, "", "  "); err == nil {
		_ = store.VersionInsert(r.Context(), metaDB, dialect, head.ID, string(snap), currentUser(r.Context()))
	}
	s.audit(r, "表单创建", body.FormName, body.TableName)
	model.OK(w, map[string]any{"reportCode": code, "formId": f.ID})
}

// handleFormDelete DELETE /online/cgform/{id}（仅删除配置，不动数据表）
func (s *Server) handleFormDelete(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	id := r.PathValue("id")
	form, err := store.FormGet(r.Context(), metaDB, dialect, id)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	if err := store.FormDelete(r.Context(), metaDB, dialect, id); err != nil {
		model.Err(w, err.Error())
		return
	}
	// 联动删除表单向导生成的报表配置（form_<表名>），避免遗留孤儿配置
	code := "form_" + strings.ToLower(form.TableName)
	if head, herr := store.HeadGetByCode(r.Context(), metaDB, dialect, code); herr == nil {
		if derr := store.HeadDelete(r.Context(), metaDB, dialect, head.ID); derr == nil {
			s.invalidateCfgCache()
		}
	}
	s.audit(r, "表单删除", form.FormName, form.TableName)
	model.OK(w, nil)
}
