// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"

	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/service"
)

// 本文件由 extra.go 按域拆分生成。

// ───── Excel 导出 ─────

// handleExportExcel 公共接口：GET /online/cgreport/api/exportExcel/{code}
func (s *Server) handleExportExcel(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	data, rowCount, err := service.ExportExcelBytes(r.Context(), target, dialect, pack.head, pack.items, pack.params, r.URL.Query())
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	s.audit(r, "导出Excel", pack.head.ReportCode, fmt.Sprintf("%d行/%d字节", rowCount, len(data)))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename="+encodeRFC5987(pack.head.ReportName+".xlsx"))
	_, _ = w.Write(data)
}

// handleImportExcel 公共接口：POST /online/cgreport/api/importExcel/{code}
// multipart 字段 file（.xlsx ≤5MB）；首行为表头（按字段名匹配，大小写不敏感），
// 数据行批量写回报表主表（事务，任一行失败整体回滚）。
func (s *Server) handleImportExcel(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, tdialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		model.Err(w, "请上传 .xlsx 文件（multipart 字段名 file）")
		return
	}
	defer file.Close()
	if hdr.Size > 5<<20 {
		model.Err(w, "文件超过 5MB 上限")
		return
	}
	xf, err := excelize.OpenReader(file)
	if err != nil {
		model.Err(w, "Excel 解析失败: "+err.Error())
		return
	}
	defer xf.Close()
	sheets := xf.GetSheetList()
	if len(sheets) == 0 {
		model.Err(w, "Excel 无工作表")
		return
	}
	grid, err := xf.GetRows(sheets[0])
	if err != nil || len(grid) < 2 {
		model.Err(w, "Excel 需包含表头和至少一行数据")
		return
	}
	// 表头 → 字段名映射（大小写不敏感，仅接受已配置字段）
	fieldByLower := map[string]string{}
	for _, it := range pack.items {
		fieldByLower[strings.ToLower(it.FieldName)] = it.FieldName
	}
	dataList := make([]map[string]any, 0, len(grid)-1)
	for _, row := range grid[1:] {
		rec := map[string]any{}
		for i, h := range grid[0] {
			name, ok := fieldByLower[strings.ToLower(strings.TrimSpace(h))]
			if !ok {
				continue // 未知列忽略
			}
			if i < len(row) {
				rec[name] = row[i]
			} else {
				rec[name] = ""
			}
		}
		if len(rec) > 0 {
			dataList = append(dataList, rec)
		}
	}
	if len(dataList) == 0 {
		model.Err(w, "没有可导入的数据行（表头需与报表字段名一致）")
		return
	}
	n, _, err := service.SaveData(r.Context(), target, tdialect, pack.head, service.SaveRequest{DataList: dataList})
	if err != nil {
		model.Err(w, "导入失败: "+err.Error())
		return
	}
	service.InvalidateCounts()
	s.audit(r, "导入Excel", code, fmt.Sprintf("%d行", n))
	model.OK(w, map[string]any{"imported": n})
}

func encodeRFC5987(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c > 127 || strings.ContainsRune("\\/:*?\"<>| ", c) {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ───── 导出 JSON / SQL INSERT ─────

// handleExportJSON 公共接口：GET /online/cgreport/api/exportJson/{code}
func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	res, err := service.QueryReport(r.Context(), target, dialect, pack.head, pack.items, pack.params, exportQuery(r.URL.Query()))
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	out, _ := json.MarshalIndent(map[string]any{
		"report": map[string]string{"name": pack.head.ReportName, "code": pack.head.ReportCode},
		"rows":   res.Records,
	}, "", "  ")
	s.audit(r, "导出JSON", code, fmt.Sprintf("%d行", len(res.Records)))
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+encodeRFC5987(pack.head.ReportCode+".json"))
	_, _ = w.Write(out)
}

// handleExportSQLInsert 公共接口：GET /online/cgreport/api/exportSQLInsert/{code}
func (s *Server) handleExportSQLInsert(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	target, dialect, pack, err := s.loadReport(r.Context(), code)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	res, err := service.QueryReport(r.Context(), target, dialect, pack.head, pack.items, pack.params, exportQuery(r.URL.Query()))
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	table, terr := service.ExtractTable(pack.head.CgSQL)
	if terr != nil {
		model.Err(w, terr.Error())
		return
	}
	var b strings.Builder
	b.WriteString("-- LiteReport 导出 INSERT 语句（表: " + table + "，行数: " + fmt.Sprint(len(res.Records)) + "）\n")
	if len(res.Records) > 0 {
		var cols []string
		for c := range res.Records[0] {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		quoted := make([]string, 0, len(cols))
		for _, c := range cols {
			quoted = append(quoted, dbx.Quote(dialect, c))
		}
		for _, rec := range res.Records {
			vals := make([]string, 0, len(cols))
			for _, c := range cols {
				vals = append(vals, sqlLiteral(rec[c]))
			}
			b.WriteString("INSERT INTO " + dbx.Quote(dialect, table) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ");\n")
		}
	}
	s.audit(r, "导出SQL", code, fmt.Sprintf("%d行", len(res.Records)))
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+encodeRFC5987(pack.head.ReportCode+".sql"))
	_, _ = w.Write([]byte(b.String()))
}

// exportQuery 统一导出查询参数（全量+汇总）。
func exportQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, vs := range q {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	out.Set("pageNo", "1")
	out.Set("pageSize", "100000")
	out.Set("needCount", "false")
	return out
}

// sqlLiteral 值转 SQL 字面量（字符串加引号转义，数字/布尔原样，NULL）。
func sqlLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if t {
			return "TRUE"
		}
		return "FALSE"
	case string:
		return "'" + strings.ReplaceAll(t, "'", "''") + "'"
	case json.Number:
		return t.String()
	case float64, float32, int, int64:
		return fmt.Sprintf("%v", t)
	default:
		return "'" + strings.ReplaceAll(fmt.Sprint(t), "'", "''") + "'"
	}
}
