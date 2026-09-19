// Package service 报表引擎：SQL 解析、参数替换、动态过滤、分页查询、公共保存。
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"litereport/internal/cache"
	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/store"
)

// Field SQL 解析出的字段元数据。
type Field struct {
	Name string `json:"name"`
	Text string `json:"text"`
	Type string `json:"type"` // 字符类型/数值类型/日期类型
}

var paramRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var fromRe = regexp.MustCompile(`(?is)\bfrom\s+["\[]?([A-Za-z_][A-Za-z0-9_.$]*)["\]]?`)
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CleanSQL 去掉结尾分号并做基础只读校验。
func CleanSQL(userSQL string) (string, error) {
	s := strings.TrimSpace(userSQL)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSuffix(s, ";")
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return "", errors.New("报表SQL不能为空")
	}
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "select") && !strings.HasPrefix(low, "with") {
		return "", errors.New("报表SQL仅支持 SELECT / WITH 查询语句")
	}
	if strings.Contains(s, ";") {
		return "", errors.New("报表SQL不允许包含分号（多条语句）")
	}
	// 占位符统一由 ${参数名} 机制生成，防止与驱动占位符编号冲突/错位
	if strings.Contains(s, "?") {
		return "", errors.New("报表SQL不允许包含 ? 占位符，动态参数请使用 ${参数名}")
	}
	return s, nil
}

// ParamNames 提取 SQL 中 ${xxx} 参数。
func ParamNames(userSQL string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, m := range paramRe.FindAllStringSubmatch(userSQL, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ParseSQL 在指定数据源上执行包一层 LIMIT 的查询，解析字段名与类型。
func ParseSQL(ctx context.Context, db *sql.DB, dialect, userSQL string) ([]Field, error) {
	s, err := CleanSQL(userSQL)
	if err != nil {
		return nil, err
	}
	if ParamNames(s) != nil {
		// 解析时给 ${} 参数填空串，保证可执行
		s, args := substituteParams(dialect, s, map[string]string{}, nil)
		wrapped := dbx.Rebind(dialect, dbx.LimitWrap(dialect, s))
		rows, err := db.QueryContext(ctx, wrapped, args...)
		if err != nil {
			return nil, fmt.Errorf("SQL解析失败: %w", err)
		}
		return columnsOf(rows, dialect)
	}
	wrapped := dbx.Rebind(dialect, dbx.LimitWrap(dialect, s))
	rows, err := db.QueryContext(ctx, wrapped)
	if err != nil {
		return nil, fmt.Errorf("SQL解析失败: %w", err)
	}
	return columnsOf(rows, dialect)
}

func columnsOf(rows *sql.Rows, dialect string) ([]Field, error) {
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make([]Field, 0, len(types))
	for _, ct := range types {
		name := ct.Name()
		if name == "litrpt_rn" {
			continue
		}
		out = append(out, Field{
			Name: strings.ToLower(name),
			Text: strings.ToLower(name),
			Type: dbx.MapType(dialect, ct.DatabaseTypeName()),
		})
	}
	return out, nil
}

// ExtractTable 从报表 SQL 中提取主表（第一个 FROM 后的表名），用于公共保存接口。
func ExtractTable(userSQL string) (string, error) {
	m := fromRe.FindStringSubmatch(userSQL)
	if m == nil || !dbx.ValidIdent(m[1]) {
		return "", errors.New("无法从报表SQL中识别数据表，公共保存接口不可用")
	}
	return m[1], nil
}

// substituteParams 把 ${name}（含 '${name}' 带引号形式）替换为绑定占位符，
// 取值优先级：请求参数 > 参数默认值 > 空串。
// 单引号字面量内的 ${x}（如 like '%${kw}%'）改写为方言拼接表达式，
// 保证所有 "?" 都落在字面量之外、与绑定参数按位对齐。
func substituteParams(dialect, userSQL string, defaults map[string]string, q map[string][]string) (string, []any) {
	resolve := func(name string) string {
		if vs, ok := q[name]; ok && len(vs) > 0 {
			return vs[0]
		}
		return defaults[name]
	}
	args := []any{}
	var b strings.Builder
	i, n := 0, len(userSQL)
	for i < n {
		if userSQL[i] == '\'' {
			// 找字面量结束位置（'' 为转义引号）
			j := i + 1
			for j < n {
				if userSQL[j] == '\'' {
					if j+1 < n && userSQL[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= n { // 引号未闭合：原样保留（后续执行时由数据库报错）
				b.WriteString(userSQL[i:])
				break
			}
			b.WriteString(rewriteLiteralParam(dialect, userSQL[i:j+1], resolve, &args))
			i = j + 1
			continue
		}
		b.WriteByte(userSQL[i])
		i++
	}
	// 字面量之外的裸 ${x} → ?
	out := paramRe.ReplaceAllStringFunc(b.String(), func(m string) string {
		name := paramRe.FindStringSubmatch(m)[1]
		args = append(args, resolve(name))
		return "?"
	})
	return out, args
}

// rewriteLiteralParam 把含 ${x} 的单引号字面量改写为 "文本 || ? || 文本" 拼接
// （MySQL 使用 CONCAT）；不含 ${x} 的字面量原样返回。
func rewriteLiteralParam(dialect, lit string, resolve func(string) string, args *[]any) string {
	if !strings.Contains(lit, "${") {
		return lit
	}
	inner := lit[1 : len(lit)-1]
	locs := paramRe.FindAllStringSubmatchIndex(inner, -1)
	if locs == nil {
		return lit
	}
	quotePart := func(s string) string {
		// s 来自原字面量的转义体（'' 形式），原样包裹即保持原语义；
		// 引号扫描保证其中不含未转义的 '
		return "'" + s + "'"
	}
	var parts []string
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			parts = append(parts, quotePart(inner[last:loc[0]]))
		}
		parts = append(parts, "?")
		*args = append(*args, resolve(inner[loc[2]:loc[3]]))
		last = loc[1]
	}
	if last < len(inner) {
		parts = append(parts, quotePart(inner[last:]))
	}
	if len(parts) == 1 {
		return parts[0] // 整个字面量就是参数值 '${x}'
	}
	if dialect == dbx.MySQL {
		return "CONCAT(" + strings.Join(parts, ", ") + ")"
	}
	return strings.Join(parts, " || ") // sqlite / postgresql / oracle
}

// buildFilters 生成外层过滤条件：
// 1) 字段配置的查询（isQuery=Y，按配置模式消费裸参数）；
// 2) 列上筛选（任意已配置字段，<col> 精确 + <col>_模式后缀），见 buildColumnFilters。
func buildFilters(dialect string, items []model.Item, q map[string][]string) (string, []any) {
	get := func(k string) string {
		if vs, ok := q[k]; ok && len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	var conds []string
	var args []any
	applied := map[string]bool{}
	for _, it := range items {
		if !strings.EqualFold(it.IsQuery, "Y") || !identRe.MatchString(it.FieldName) {
			continue
		}
		applied[strings.ToLower(it.FieldName)] = true
		col := dbx.Quote(dialect, it.FieldName)
		mode := it.QueryMode
		if mode == "" {
			mode = "="
		}
		switch strings.ToLower(mode) {
		case "between":
			lo, hi := get(it.FieldName+"_begin"), get(it.FieldName+"_end")
			if lo == "" && hi == "" {
				if v := get(it.FieldName); strings.Contains(v, ",") {
					parts := strings.SplitN(v, ",", 2)
					lo, hi = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
				}
			}
			cond, cargs := betweenCond(col, lo, hi)
			if cond != "" {
				conds = append(conds, cond)
				args = append(args, cargs...)
			}
		case "in":
			v := get(it.FieldName)
			if v == "" {
				continue
			}
			c, cargs := inCond(col, v)
			conds = append(conds, c)
			args = append(args, cargs...)
		case "like", "not like":
			v := get(it.FieldName)
			if v == "" {
				continue
			}
			conds = append(conds, fmt.Sprintf("%s %s ?", col, strings.ToUpper(mode)))
			args = append(args, "%"+v+"%")
		case "=", "!=", ">", ">=", "<", "<=":
			v := get(it.FieldName)
			if v == "" {
				continue
			}
			conds = append(conds, fmt.Sprintf("%s %s ?", col, mode))
			args = append(args, v)
		}
	}

	// 列上筛选：任意已配置字段
	cCond, cArgs := buildColumnFilters(dialect, items, q, applied)
	conds = append(conds, cCond...)
	args = append(args, cArgs...)

	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// colFilterModes 列上筛选的模式后缀白名单（裸 <col> 为精确匹配）。
var colFilterModes = map[string]string{
	"ne": "!=", "gt": ">", "ge": ">=", "lt": "<", "le": "<=",
	"like": " LIKE ", "in": " IN ", "bw": " BETWEEN ",
}

// colFilterReserved 保留参数，不参与列上筛选。
var colFilterReserved = map[string]bool{
	"pageNo": true, "pageSize": true, "column": true, "order": true,
	"needCount": true, "needSummary": true,
}

// buildColumnFilters 解析列上筛选参数：
//   - <col>=v          精确（isQuery 字段的裸参数已被配置模式消费，跳过）
//   - <col>_like=v     模糊
//   - <col>_in=a,b     枚举
//   - <col>_bw=a,b     区间
//   - <col>_ne/_gt/_ge/_lt/_le=v
//
// 列名必须在字段配置中且为合法标识符，操作符走白名单，值全部绑定。
func buildColumnFilters(dialect string, items []model.Item, q map[string][]string, applied map[string]bool) ([]string, []any) {
	byField := map[string]model.Item{}
	for _, it := range items {
		if identRe.MatchString(it.FieldName) {
			byField[strings.ToLower(it.FieldName)] = it
		}
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if colFilterReserved[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var conds []string
	var args []any
	for _, k := range keys {
		vs := q[k]
		if len(vs) == 0 || vs[0] == "" {
			continue
		}
		v := vs[0]
		col, mode := k, ""
		if i := strings.LastIndex(k, "_"); i > 0 {
			if _, isMode := colFilterModes[k[i+1:]]; isMode {
				col, mode = k[:i], k[i+1:]
			}
		}
		it, ok := byField[strings.ToLower(col)]
		if !ok {
			continue // 列不在字段配置中，忽略（防注入）
		}
		c := dbx.Quote(dialect, it.FieldName)
		switch mode {
		case "":
			if applied[strings.ToLower(col)] {
				continue
			}
			conds = append(conds, c+" = ?")
			args = append(args, v)
		case "like":
			conds = append(conds, c+" LIKE ?")
			args = append(args, "%"+v+"%")
		case "in":
			cd, cargs := inCond(c, v)
			conds = append(conds, cd)
			args = append(args, cargs...)
		case "bw":
			lo, hi := v, ""
			if i := strings.Index(v, ","); i >= 0 {
				lo, hi = v[:i], v[i+1:]
			}
			if cd, cargs := betweenCond(c, lo, hi); cd != "" {
				conds = append(conds, cd)
				args = append(args, cargs...)
			}
		default: // ne/gt/ge/lt/le
			conds = append(conds, fmt.Sprintf("%s %s ?", c, colFilterModes[mode]))
			args = append(args, v)
		}
	}
	return conds, args
}

func inCond(col, v string) (string, []any) {
	parts := strings.Split(v, ",")
	ph := strings.TrimSuffix(strings.Repeat("?,", len(parts)), ",")
	args := make([]any, 0, len(parts))
	for _, p := range parts {
		args = append(args, strings.TrimSpace(p))
	}
	return fmt.Sprintf("%s IN (%s)", col, ph), args
}

func betweenCond(col, lo, hi string) (string, []any) {
	switch {
	case lo == "" && hi == "":
		return "", nil
	case lo == "":
		return col + " <= ?", []any{hi}
	case hi == "":
		return col + " >= ?", []any{lo}
	default:
		return col + " BETWEEN ? AND ?", []any{lo, hi}
	}
}

// QueryResult 公共查询结果。
type QueryResult struct {
	Records   []map[string]any
	Total     int // needCount=false 时为 -1
	Columns   []string
	Summary   map[string]any // 合计列汇总（needSummary=true 且配置了合计列时非空）
	ElapsedMS int64          // 查询耗时（毫秒），供慢查询台账使用
}

// buildReportQuery 构造过滤后的数据子查询（SQL清洗 → ${param}替换 → 动态过滤 → 行级权限），
// 供 QueryReport 与 Excel 导出共用；items 为空时自动解析并回填。
func buildReportQuery(ctx context.Context, db *sql.DB, dialect string, head *model.Head, items []model.Item, params []model.Param, q map[string][]string) (string, []any, []model.Item, error) {
	base, err := CleanSQL(head.CgSQL)
	if err != nil {
		return "", nil, items, err
	}
	defaults := map[string]string{}
	for _, p := range params {
		defaults[p.ParamName] = p.ParamValue
	}
	inner, args := substituteParams(dialect, base, defaults, q)

	// 未配置字段明细时自动解析（结果缓存，避免每次请求都探测）
	if len(items) == 0 {
		items = cachedItems(ctx, db, dialect, head.CgSQL)
	}

	filterSQL, fargs := buildFilters(dialect, items, q)
	args = append(args, fargs...)
	dataSQL := "SELECT * FROM ( " + inner + " ) litrpt_data" + filterSQL

	// 行级数据权限：__perm 由服务端注入（head.PermFilter 渲染 {user} 后），非用户输入
	if perm := strings.TrimSpace(firstOf(q, "__perm")); perm != "" {
		if filterSQL == "" {
			dataSQL += " WHERE " + perm
		} else {
			dataSQL += " AND (" + perm + ")"
		}
	}
	return dataSQL, args, items, nil
}

// orderClauseOf 排序子句：列名必须存在于字段配置，防止注入。
func orderClauseOf(dialect string, items []model.Item, q map[string][]string) string {
	sortCol, sortDir := strings.TrimSpace(firstOf(q, "column")), strings.ToLower(firstOf(q, "order"))
	if sortCol != "" && identRe.MatchString(sortCol) {
		for _, it := range items {
			if strings.EqualFold(it.FieldName, sortCol) {
				if sortDir != "asc" && sortDir != "desc" {
					sortDir = "asc"
				}
				return fmt.Sprintf(" ORDER BY %s %s", dbx.Quote(dialect, sortCol), strings.ToUpper(sortDir))
			}
		}
	}
	return ""
}

// QueryReport 执行公共查询：参数替换 + 条件过滤(含列上筛选) + 排序 + 分页 + 合计汇总。
// 性能：COUNT/SUM 合并为一次聚合扫描且带 10s TTL 缓存；字段解析带 60s TTL 缓存；
// 请求可传 needCount=false 跳过计数、needSummary=true 获取合计列汇总。
func QueryReport(ctx context.Context, db *sql.DB, dialect string, head *model.Head, items []model.Item, params []model.Param, q map[string][]string) (*QueryResult, error) {
	started := time.Now()
	dataSQL, args, items, err := buildReportQuery(ctx, db, dialect, head, items, params, q)
	if err != nil {
		return nil, err
	}

	// 排序：与 COUNT/SUM 聚合查询分离
	orderClause := orderClauseOf(dialect, items, q)

	page := 1
	size := 10
	if v := firstOf(q, "pageNo"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			page = n
		}
	}
	if v := firstOf(q, "pageSize"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			size = n
		}
	}
	if size > 100000 {
		size = 100000
	}

	needCount := strings.ToLower(firstOf(q, "needCount")) != "false"
	// 合计列汇总：对配置了“合计列”的数值字段做 SUM（全量结果集，非当前页）
	needSummary := strings.ToLower(firstOf(q, "needSummary")) == "true"
	var sumCols []string
	if needSummary {
		for _, it := range items {
			if strings.EqualFold(it.IsTotal, "Y") && it.FieldType == "数值类型" && identRe.MatchString(it.FieldName) {
				sumCols = append(sumCols, it.FieldName)
			}
		}
	}
	total := -1
	var summary map[string]any
	// COUNT 与 SUM 合并为一次聚合扫描（此前最多各跑一遍全量子查询）
	if needCount || len(sumCols) > 0 {
		var sumRes map[string]any
		total, sumRes, err = cachedAgg(ctx, db, dialect, dataSQL, args, sumCols, needCount)
		if err != nil {
			return nil, fmt.Errorf("查询失败: %w", err)
		}
		if len(sumCols) > 0 {
			summary = sumRes
		}
	}

	pageSQL := dbx.Rebind(dialect, dbx.PageWrap(dialect, dataSQL+orderClause, page, size))
	rows, err := db.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		if c == "litrpt_rn" {
			continue
		}
		names = append(names, strings.ToLower(c))
	}
	records := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := map[string]any{}
		for i, c := range cols {
			if c == "litrpt_rn" {
				continue
			}
			rec[strings.ToLower(c)] = normalizeVal(vals[i])
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &QueryResult{Records: records, Total: total, Columns: names, Summary: summary, ElapsedMS: time.Since(started).Milliseconds()}, nil
}

func firstOf(q map[string][]string, k string) string {
	if vs, ok := q[k]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// ───── 热点缓存：COUNT 与字段解析 ─────

var countCache = cache.New()
var sniffCache = cache.New()

const (
	countCacheTTL = 10 * time.Second
	sniffCacheTTL = 60 * time.Second
)

// InvalidateCounts 写操作（saveData/deleteData）后使 COUNT/SUM 缓存失效，保证总数与合计及时一致。
func InvalidateCounts() { countCache.DeletePrefix("cnt:") }

// aggResult 聚合查询缓存值：总数 + 合计列。
type aggResult struct {
	Total   int
	Summary map[string]any
}

// cachedAgg COUNT 与 SUM 合并为一次聚合扫描（SELECT COUNT(*), SUM(a), SUM(b) ...），
// 避免同一条请求对内层全量子查询重复执行；带 10s TTL 缓存，写操作后由 InvalidateCounts 失效。
func cachedAgg(ctx context.Context, db *sql.DB, dialect, dataSQL string, args []any, sumCols []string, withCount bool) (int, map[string]any, error) {
	key := "cnt:agg:" + dialect + ":" + hashKey(dataSQL, args, sumCols, withCount)
	if v, ok := countCache.Get(key); ok {
		if a, ok2 := v.(aggResult); ok2 {
			return a.Total, a.Summary, nil
		}
	}
	selects := make([]string, 0, len(sumCols)+1)
	if withCount {
		selects = append(selects, "COUNT(*) AS litrpt_cnt")
	}
	for _, c := range sumCols {
		qc := dbx.Quote(dialect, c)
		selects = append(selects, "SUM("+qc+") AS "+qc)
	}
	if len(selects) == 0 { // 仅汇总但无可用合计列：空结果
		countCache.Set(key, aggResult{Total: -1, Summary: nil}, countCacheTTL)
		return -1, nil, nil
	}
	q := dbx.Rebind(dialect, "SELECT "+strings.Join(selects, ", ")+" FROM ( "+dataSQL+" ) litrpt_agg")
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	total := -1
	if rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return 0, nil, err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, nil, err
		}
		for i, c := range cols {
			if strings.EqualFold(c, "litrpt_cnt") {
				if n, ok := normalizeVal(vals[i]).(int64); ok {
					total = int(n)
				}
				continue
			}
			if vals[i] != nil {
				out[strings.ToLower(c)] = normalizeVal(vals[i])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	countCache.Set(key, aggResult{Total: total, Summary: out}, countCacheTTL)
	return total, out, nil
}

// cachedItems 缓存自动解析的字段明细（报表未配置明细时的兜底路径）。
func cachedItems(ctx context.Context, db *sql.DB, dialect, userSQL string) []model.Item {
	key := "sniff:" + dialect + ":" + hashKey(userSQL)
	if v, ok := sniffCache.Get(key); ok {
		if items, ok2 := v.([]model.Item); ok2 {
			return items
		}
	}
	items := []model.Item{}
	if fs, err := ParseSQL(ctx, db, dialect, userSQL); err == nil {
		for i, f := range fs {
			items = append(items, model.Item{
				FieldName: f.Name, FieldTxt: f.Text, FieldType: f.Type,
				IsShow: "Y", IsQuery: "N", QueryMode: "=", OrderNum: i,
			})
		}
		sniffCache.Set(key, items, sniffCacheTTL)
	}
	return items
}

func hashKey(parts ...any) string {
	h := sha256.New()
	fmt.Fprintf(h, "%v", parts)
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func normalizeVal(v any) any {
	switch t := v.(type) {
	case time.Time:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
			return t.Format("2006-01-02")
		}
		return t.Format("2006-01-02 15:04:05")
	case []byte:
		return string(t)
	default:
		return v
	}
}

// SaveRequest 公共保存接口请求体。
type SaveRequest struct {
	Data     map[string]any   `json:"data"`
	DataList []map[string]any `json:"dataList"`
	Table    string           `json:"table"` // 可选：显式指定保存表
}

// rowExecer saveRow 的执行器抽象：单条时为 *sql.DB，批量时为事务 *sql.Tx。
type rowExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SaveData 公共保存：data.id 有值则更新，否则插入；返回保存条数与生成的 id。
// 批量 dataList 整体包事务：任一行失败全部回滚，不会留下半提交状态。
func SaveData(ctx context.Context, db *sql.DB, dialect string, head *model.Head, req SaveRequest) (int, []string, error) {
	table := req.Table
	if table == "" {
		t, err := ExtractTable(head.CgSQL)
		if err != nil {
			return 0, nil, err
		}
		table = t
	}
	// fromRe 捕获组本身不含引号；显式传入的 table 直接做白名单校验，
	// 任何引号/特殊字符都在这里被拒绝（先校验后使用，不做剥引号等预处理）
	parts := strings.Split(table, ".")
	for _, p := range parts {
		if !identRe.MatchString(p) {
			return 0, nil, fmt.Errorf("数据表名[%s]不合法", table)
		}
	}
	rows := req.DataList
	if req.Data != nil {
		rows = append(rows, req.Data)
	}
	if len(rows) == 0 {
		return 0, nil, errors.New("保存内容为空：请传 data 或 dataList")
	}
	var tx *sql.Tx
	runner := rowExecer(db)
	if len(rows) > 1 {
		t, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, nil, err
		}
		tx = t
		runner = t
		defer tx.Rollback() // Commit 后为 no-op
	}
	ids := []string{}
	saved := 0
	for _, row := range rows {
		id, err := saveRow(ctx, runner, dialect, table, row)
		if err != nil {
			return saved, ids, fmt.Errorf("第%d条保存失败: %w", saved+1, err)
		}
		ids = append(ids, id)
		saved++
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return 0, nil, err
		}
	}
	return saved, ids, nil
}

func saveRow(ctx context.Context, ex rowExecer, dialect, table string, row map[string]any) (string, error) {
	if len(row) == 0 {
		return "", errors.New("数据字段为空")
	}
	cols := make([]string, 0, len(row))
	hasID := false
	for c := range row {
		if !identRe.MatchString(c) {
			return "", fmt.Errorf("字段名[%s]不合法", c)
		}
		if strings.EqualFold(c, "id") {
			hasID = true
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)

	id := ""
	if v, ok := row["id"]; ok && v != nil {
		id = strings.TrimSpace(fmt.Sprintf("%v", v))
	}
	insertMode := true
	if id != "" {
		var exists int
		if err := ex.QueryRowContext(ctx, dbx.Rebind(dialect,
			"SELECT COUNT(*) FROM "+dbx.Quote(dialect, table)+" WHERE "+dbx.Quote(dialect, "id")+" = ?"), id).Scan(&exists); err != nil {
			return "", err
		}
		insertMode = exists == 0
	} else {
		id = newUUID()
	}
	if insertMode && !hasID {
		cols = append(cols, "id")
		sort.Strings(cols)
	}

	tq := dbx.Quote(dialect, table)
	valOf := func(c string) any {
		if strings.EqualFold(c, "id") {
			return id
		}
		return JSONValue(row[c])
	}
	if !insertMode {
		sets := make([]string, 0, len(cols))
		args := make([]any, 0, len(cols)+1)
		for _, c := range cols {
			sets = append(sets, dbx.Quote(dialect, c)+" = ?")
			args = append(args, valOf(c))
		}
		args = append(args, id)
		q := "UPDATE " + tq + " SET " + strings.Join(sets, ", ") +
			" WHERE " + dbx.Quote(dialect, "id") + " = ?"
		_, err := ex.ExecContext(ctx, dbx.Rebind(dialect, q), args...)
		return id, err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	quoted := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, dbx.Quote(dialect, c))
		args = append(args, valOf(c))
	}
	q := "INSERT INTO " + tq + " (" + strings.Join(quoted, ", ") + ") VALUES (" + ph + ")"
	if _, err := ex.ExecContext(ctx, dbx.Rebind(dialect, q), args...); err != nil {
		return "", err
	}
	return id, nil
}

// DeleteData 公共删除：按主键 id 批量删除。
func DeleteData(ctx context.Context, db *sql.DB, dialect string, head *model.Head, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, errors.New("请指定要删除的数据 id")
	}
	table, err := ExtractTable(head.CgSQL)
	if err != nil {
		return 0, err
	}
	parts := strings.Split(table, ".")
	for _, p := range parts {
		if !identRe.MatchString(p) {
			return 0, fmt.Errorf("数据表名[%s]不合法", table)
		}
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	q := "DELETE FROM " + dbx.Quote(dialect, table) + " WHERE " + dbx.Quote(dialect, "id") + " IN (" + ph + ")"
	args := make([]any, 0, len(ids))
	for _, v := range ids {
		args = append(args, v)
	}
	res, err := db.ExecContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// newUUID 复用 store 层的随机 ID 生成器。
func newUUID() string { return store.NewID() }

// JSONValue 把 json.Number 及嵌套结构转换为驱动可接受的值。
func JSONValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, err := t.Float64()
		if err == nil {
			return f
		}
		return t.String()
	case map[string]any, []any:
		b, err := json.Marshal(t)
		if err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", t)
	case nil:
		return nil
	default:
		return v
	}
}
