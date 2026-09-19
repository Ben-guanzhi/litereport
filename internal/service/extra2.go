// P1/P2 第二批服务：交叉表、SQL试运行、表浏览器、webhook推送、流式Excel、图表缓存。
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"litereport/internal/cache"
	"litereport/internal/dbx"
	"litereport/internal/model"
)

// ───── 交叉表（行转列）─────

// CrossTable 交叉表结果：columns 为列字段去重值，rows 为行字段值 + 各列聚合值。
type CrossTable struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"` // 聚合组数超过上限被截断
}

// crossTableMaxRows 交叉表聚合行数上限（防高基数维度拉爆内存/响应体）。
var crossTableMaxRows = 5000

// CrossTableData 查询 row/col 两个字段分组聚合后，在内存中透视。
func CrossTableData(ctx context.Context, db *sql.DB, dialect string, head *model.Head, items []model.Item, params []model.Param, q map[string][]string,
	rowField, colField, measure, agg string, maxCols int) (*CrossTable, error) {
	base, err := CleanSQL(head.CgSQL)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		items = cachedItems(ctx, db, dialect, head.CgSQL)
	}
	validField := validFieldOf(items)
	if !validField(rowField) || !validField(colField) {
		return nil, fmt.Errorf("行/列字段必须在报表字段中")
	}
	aggFn, ok := chartAggs[strings.ToLower(agg)]
	if !ok {
		aggFn = "SUM"
	}
	if measure == "" || strings.EqualFold(aggFn, "COUNT") {
		measure, aggFn = "*", "COUNT"
	} else if !validField(measure) {
		return nil, fmt.Errorf("度量字段[%s]不在报表字段中", measure)
	}
	defaults := map[string]string{}
	for _, p := range params {
		defaults[p.ParamName] = p.ParamValue
	}
	inner, args := substituteParams(dialect, base, defaults, q)
	if maxCols <= 0 || maxCols > 50 {
		maxCols = 12
	}
	sqlText := fmt.Sprintf(
		"SELECT %s AS litrpt_r, %s AS litrpt_c, %s(%s) AS litrpt_v FROM ( %s ) litrpt_cross GROUP BY %s, %s",
		dbx.Quote(dialect, rowField), dbx.Quote(dialect, colField), aggFn, measure, inner,
		dbx.Quote(dialect, rowField), dbx.Quote(dialect, colField))
	sqlText = dbx.LimitWrapN(dialect, sqlText, crossTableMaxRows+1) // 多取一行用于判定截断
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, sqlText), args...)
	if err != nil {
		return nil, fmt.Errorf("交叉表查询失败: %w", err)
	}
	defer rows.Close()
	colOrder := []string{}
	colSet := map[string]bool{}
	cell := map[string]any{} // "r\x00c" -> value
	rowOrder := []string{}
	rowSet := map[string]bool{}
	truncated := false
	for rows.Next() {
		var rv, cv, v any
		if err := rows.Scan(&rv, &cv, &v); err != nil {
			return nil, err
		}
		if len(rowOrder) >= crossTableMaxRows {
			truncated = true
			break
		}
		rName := fmtCell(normalizeVal(rv))
		cName := fmtCell(normalizeVal(cv))
		if !rowSet[rName] {
			rowSet[rName] = true
			rowOrder = append(rowOrder, rName)
		}
		if !colSet[cName] && len(colOrder) < maxCols {
			colSet[cName] = true
			colOrder = append(colOrder, cName)
		}
		cell[rName+"\x00"+cName] = normalizeVal(v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &CrossTable{Columns: colOrder, Rows: []map[string]any{}, Truncated: truncated}
	for _, r := range rowOrder {
		m := map[string]any{"__row": r}
		for _, c := range colOrder {
			m[c] = cell[r+"\x00"+c]
		}
		out.Rows = append(out.Rows, m)
	}
	return out, nil
}

// ───── SQL 试运行（编辑弹窗内联预览）─────

// PreviewSQL 执行报表SQL前N行，返回列名与记录（临时报表，不落配置）。
func PreviewSQL(ctx context.Context, db *sql.DB, dialect, userSQL string, n int) ([]string, []map[string]any, error) {
	if n <= 0 || n > 50 {
		n = 10
	}
	head := &model.Head{CgSQL: userSQL}
	q := map[string][]string{"pageNo": {"1"}, "pageSize": {fmt.Sprint(n)}, "needCount": {"false"}}
	res, err := QueryReport(ctx, db, dialect, head, nil, nil, q)
	if err != nil {
		return nil, nil, err
	}
	return res.Columns, res.Records, nil
}

// ───── 表浏览器（数据源表/列元数据）─────

// ListTables 按方言列出数据表。
func ListTables(ctx context.Context, db *sql.DB, dialect string) ([]string, error) {
	var q string
	switch dialect {
	case dbx.SQLite:
		q = `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'onl_%' AND name NOT LIKE 'sys_dict' AND name NOT LIKE 'platform_%' ORDER BY name`
	case dbx.MySQL:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() ORDER BY table_name`
	case dbx.PostgreSQL:
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY table_name`
	case dbx.Oracle:
		q = `SELECT table_name FROM user_tables ORDER BY table_name`
	case dbx.SQLServer:
		q = `SELECT name FROM sys.tables ORDER BY name`
	case dbx.ClickHouse:
		q = `SELECT name FROM system.tables WHERE database=currentDatabase() ORDER BY name`
	default:
		return nil, fmt.Errorf("方言[%s]暂不支持表浏览", dialect)
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// TableColumn 表列元数据。
type TableColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListColumns 按方言列出表的列。
func ListTableColumns(ctx context.Context, db *sql.DB, dialect, table string) ([]TableColumn, error) {
	if !dbx.ValidIdent(table) {
		return nil, fmt.Errorf("表名不合法")
	}
	var q string
	var args []any
	switch dialect {
	case dbx.SQLite:
		q = `SELECT name, type FROM pragma_table_info(?) ORDER BY cid`
		args = append(args, table)
	case dbx.MySQL:
		q = `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`
		args = append(args, table)
	case dbx.PostgreSQL:
		q = `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema='public' AND table_name=? ORDER BY ordinal_position`
		args = append(args, table)
	case dbx.Oracle:
		q = `SELECT column_name, data_type FROM user_tab_columns WHERE table_name=UPPER(?) ORDER BY column_id`
		args = append(args, table)
	case dbx.SQLServer:
		q = `SELECT c.name, t.name FROM sys.columns c JOIN sys.types t ON c.user_type_id=t.user_type_id WHERE c.object_id=OBJECT_ID(?) ORDER BY c.column_id`
		args = append(args, table)
	case dbx.ClickHouse:
		q = `SELECT name, type FROM system.columns WHERE database=currentDatabase() AND table=? ORDER BY position`
		args = append(args, table)
	default:
		return nil, fmt.Errorf("方言[%s]暂不支持列浏览", dialect)
	}
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableColumn{}
	for rows.Next() {
		var c TableColumn
		if err := rows.Scan(&c.Name, &c.Type); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// ───── Webhook 推送（钉钉/企微机器人）─────

// SendWebhook 推送 markdown 卡片到钉钉/企业微信群机器人。
func SendWebhook(ctx context.Context, channel, webhookURL, title, markdown string) error {
	if webhookURL == "" {
		return fmt.Errorf("webhook地址为空")
	}
	var payload map[string]any
	switch strings.ToLower(channel) {
	case "dingtalk":
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"title": title, "text": markdown},
		}
	case "wecom":
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]string{"content": markdown},
		}
	default:
		return fmt.Errorf("不支持的推送渠道: %s", channel)
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook响应: %s", resp.Status)
	}
	return nil
}

// ───── 流式 Excel（大数据量导出）─────

const streamExcelThreshold = 3000

// ───── 图表数据缓存 ─────

var chartCache = cache.New()

// ChartCacheKey 图表缓存键。
func ChartCacheKey(parts ...any) string { return "chart:" + hashKey(parts...) }

// ChartCacheGet / ChartCacheSet 供 handler 使用（30s TTL）。
func ChartCacheGet(key string) ([]ChartPoint, bool) {
	if v, ok := chartCache.Get(key); ok {
		if pts, ok2 := v.([]ChartPoint); ok2 {
			return pts, true
		}
	}
	return nil, false
}

func ChartCacheSet(key string, pts []ChartPoint) {
	chartCache.Set(key, pts, 30*time.Second)
}

// ───── 危险 SQL 检测（试运行前的确认机制）─────

var dangerousRules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)\binto\s+(outfile|dumpfile)\b`), "包含 INTO OUTFILE/DUMPFILE（可写服务器文件）"},
	{regexp.MustCompile(`(?i)\binto\s+["` + "`" + `\[]?[a-zA-Z_#]`), "包含 SELECT INTO（会创建表/写文件）"},
	{regexp.MustCompile(`(?i)\b(pg_sleep|sleep|load_file|pg_read_file|pg_ls_dir|xp_cmdshell|dbms_lock)\s*\(`), "包含高危函数"},
	{regexp.MustCompile(`(?i)\b(attach|detach)\s+database\b`), "包含 ATTACH/DETACH DATABASE"},
	{regexp.MustCompile(`(?i)\bcopy\s+.*\bfrom\s+program\b`), "包含 COPY FROM PROGRAM"},
}

// CheckDangerousSQL 返回命中的危险特征（空=正常）。
func CheckDangerousSQL(userSQL string) []string {
	var reasons []string
	for _, r := range dangerousRules {
		if r.re.MatchString(userSQL) {
			reasons = append(reasons, r.reason)
		}
	}
	return reasons
}
