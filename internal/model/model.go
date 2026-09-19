// Package model 定义平台数据结构与统一响应。
package model

import (
	"encoding/json"
	"net/http"
	"time"
)

// Head 报表配置头（对应 jeecg onl_cgreport_head）。
type Head struct {
	ID          string `json:"id"`
	ReportName  string `json:"reportName"`
	ReportCode  string `json:"reportCode"`
	CgSQL       string `json:"cgSql"`
	DbType      string `json:"dbType,omitempty"` // 兼容字段，未使用
	DbSource    string `json:"dbSource"`         // 动态数据源 key
	Category    string `json:"category"`         // 报表分类（列表筛选/分组展示）
	ShareToken  string `json:"-"`                // 分享链接令牌（不外露）
	ShareExpire string `json:"-"`                // 分享过期时间（空=永久）
	PermFilter  string `json:"permFilter"`       // 行级数据权限过滤（{user} 占位当前用户名）
	PermEnabled string `json:"permEnabled"`      // Y/N
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
	CreateBy    string `json:"createBy"`
	UpdateBy    string `json:"updateBy"`
}

// Item 报表字段明细（对应 onl_cgreport_item）。
type Item struct {
	ID         string `json:"id"`
	HeadID     string `json:"cgrheadId"`
	FieldName  string `json:"fieldName"`
	FieldTxt   string `json:"fieldTxt"`
	FieldType  string `json:"fieldType"` // 字符类型/数值类型/日期类型
	FieldHref  string `json:"fieldHref"`
	IsShow     string `json:"isShow"` // Y/N
	IsQuery    string `json:"isQuery"`
	QueryMode  string `json:"queryMode"` // =,!=,like,in,between,>,>=,<,<=
	DictCode   string `json:"dictCode"`
	GroupTitle string `json:"groupTitle"`
	OrderNum   int    `json:"orderNum"`
	Width      int    `json:"width"`
	IsTotal    string `json:"isTotal"`
	RenderRule string `json:"renderRule"` // 渲染规则 JSON：{"lt":0}红 {"gt":100}绿
}

// Param 报表参数（对应 onl_cgreport_param，SQL 中 ${name}）。
type Param struct {
	ID         string `json:"id"`
	HeadID     string `json:"cgrheadId"`
	ParamName  string `json:"paramName"`
	ParamTxt   string `json:"paramTxt"`
	ParamValue string `json:"paramValue"` // 默认值
	OrderNum   int    `json:"orderNum"`
}

// Page 分页结果。
type Page struct {
	Records any            `json:"records"`
	Total   int            `json:"total"`
	Size    int            `json:"size"`
	Current int            `json:"current"`
	Pages   int            `json:"pages"`
	Summary map[string]any `json:"summary,omitempty"` // 合计列汇总（needSummary=true 且配置了合计列时返回）
}

type envelope struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Code      int    `json:"code"`
	Result    any    `json:"result,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func OK(w http.ResponseWriter, result any) {
	WriteJSON(w, http.StatusOK, envelope{Success: true, Message: "操作成功", Code: 200, Result: result, Timestamp: time.Now().UnixMilli()})
}

func Err(w http.ResponseWriter, msg string) {
	WriteJSON(w, http.StatusOK, envelope{Success: false, Message: msg, Code: 500, Timestamp: time.Now().UnixMilli()})
}

func ErrStatus(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, envelope{Success: false, Message: msg, Code: status, Timestamp: time.Now().UnixMilli()})
}
