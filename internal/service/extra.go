// P1/P2 扩展服务：Excel 导出、图表聚合、表单建表、AI 生成 SQL、定时报表推送。
package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/smtp"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/store"
)

// ───── Excel 导出 ─────

// ExportExcelBytes 按当前查询条件导出全量数据为 xlsx（含合计行与基础样式）。
// 流式实现：直接以 *sql.Rows 逐行写 StreamWriter，不再把全部记录物化到内存；
// 返回值附带数据行数（供推送摘要等使用，免去重复解压 xlsx 计数）。
func ExportExcelBytes(ctx context.Context, db *sql.DB, dialect string, head *model.Head, items []model.Item, params []model.Param, q map[string][]string) ([]byte, int, error) {
	qc := map[string][]string{}
	for k, v := range q {
		qc[k] = v
	}
	qc["needCount"] = []string{"false"}
	qc["needSummary"] = []string{"true"}
	qc["pageNo"] = []string{"1"}
	qc["pageSize"] = []string{"100000"}

	dataSQL, args, items, err := buildReportQuery(ctx, db, dialect, head, items, params, qc)
	if err != nil {
		return nil, 0, err
	}
	pageSQL := dbx.Rebind(dialect, dbx.PageWrap(dialect, dataSQL+orderClauseOf(dialect, items, qc), 1, 100000))
	rows, err := db.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	// 展示列：优先字段明细，未配置时按结果集列名兜底
	show := filterShow(items)
	var summary map[string]any
	if len(show) == 0 {
		cols, err := rows.Columns()
		if err != nil {
			return nil, 0, err
		}
		for _, c := range cols {
			if c == "litrpt_rn" {
				continue
			}
			show = append(show, model.Item{FieldName: c, FieldTxt: c})
		}
		sort.Slice(show, func(i, j int) bool { return show[i].FieldName < show[j].FieldName })
		// 兜底列的合计：重新以聚合查询求值
		var sumCols []string
		for _, it := range show {
			if identRe.MatchString(it.FieldName) {
				sumCols = append(sumCols, it.FieldName)
			}
		}
		if len(sumCols) > 0 {
			_, summary, _ = cachedAgg(ctx, db, dialect, dataSQL, args, sumCols, false)
		}
	} else {
		var sumCols []string
		for _, it := range show {
			if strings.EqualFold(it.IsTotal, "Y") && it.FieldType == "数值类型" && identRe.MatchString(it.FieldName) {
				sumCols = append(sumCols, it.FieldName)
			}
		}
		if len(sumCols) > 0 {
			_, summary, _ = cachedAgg(ctx, db, dialect, dataSQL, args, sumCols, false)
		}
	}

	f := excelize.NewFile()
	sheet := sanitizeSheetName(head.ReportName)
	f.SetSheetName("Sheet1", sheet)
	// 表头样式：teal 底 + 白字加粗
	hs, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#13A8A8"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return nil, 0, err
	}
	headCells := make([]any, len(show))
	for i, it := range show {
		txt := it.FieldTxt
		if txt == "" {
			txt = it.FieldName
		}
		headCells[i] = excelize.Cell{Value: txt}
	}
	if err := sw.SetRow("A1", headCells, excelize.RowOpts{StyleID: hs}); err != nil {
		return nil, 0, err
	}

	cols, err := rows.Columns()
	if err != nil {
		return nil, 0, err
	}
	rowCount := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 0, err
		}
		rec := map[string]any{}
		for i, c := range cols {
			if c == "litrpt_rn" {
				continue
			}
			rec[strings.ToLower(c)] = vals[i]
		}
		cells := make([]any, len(show))
		for i, it := range show {
			cells[i] = excelize.Cell{Value: fmtCell(rec[it.FieldName])}
		}
		cell, _ := excelize.CoordinatesToCellName(1, rowCount+2)
		if err := sw.SetRow(cell, cells); err != nil {
			return nil, 0, err
		}
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// 合计行（全量结果集汇总）
	if len(summary) > 0 {
		total := make([]any, len(show))
		total[0] = excelize.Cell{Value: "合计"}
		for i, it := range show {
			if v, ok := summary[it.FieldName]; ok && v != nil {
				total[i] = excelize.Cell{Value: fmtCell(v)}
			}
		}
		cell, _ := excelize.CoordinatesToCellName(1, rowCount+2)
		if err := sw.SetRow(cell, total); err != nil {
			return nil, 0, err
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, 0, err
	}
	// 列宽按表头估算（流式写入不再回扫数据；表头文字越长列越宽，上限40）
	for i, it := range show {
		txt := it.FieldTxt
		if txt == "" {
			txt = it.FieldName
		}
		w := float64(len([]rune(txt))) + 4
		if w > 40 {
			w = 40
		}
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheet, col, col, w+4)
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), rowCount, nil
}

func filterShow(items []model.Item) []model.Item {
	out := make([]model.Item, 0, len(items))
	for _, it := range items {
		if it.IsShow != "N" {
			out = append(out, it)
		}
	}
	return out
}

func cellName(col, row int) string {
	c, _ := excelize.CoordinatesToCellName(col, row)
	return c
}

var sheetBad = regexp.MustCompile(`[:\\/?*\[\]]`)

func sanitizeSheetName(s string) string {
	s = sheetBad.ReplaceAllString(s, "_")
	if s == "" {
		s = "报表"
	}
	if len([]rune(s)) > 28 {
		s = string([]rune(s)[:28])
	}
	return s
}

// ───── 图表聚合 ─────

// ChartPoint 图表数据点。
type ChartPoint struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

var chartAggs = map[string]string{"sum": "SUM", "count": "COUNT", "avg": "AVG", "max": "MAX", "min": "MIN"}

// validFieldOf 返回校验“字段名是否在报表字段列表中”的闭包（图表/交叉表共用）。
func validFieldOf(items []model.Item) func(string) bool {
	return func(name string) bool {
		if !identRe.MatchString(name) {
			return false
		}
		for _, it := range items {
			if strings.EqualFold(it.FieldName, name) {
				return true
			}
		}
		return false
	}
}

// ChartData 按维度分组聚合（作用于报表SQL结果集），供图表/大屏使用。
func ChartData(ctx context.Context, db *sql.DB, dialect string, head *model.Head, items []model.Item, params []model.Param, q map[string][]string, dim, measure, agg string, limit int) ([]ChartPoint, error) {
	base, err := CleanSQL(head.CgSQL)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		items = cachedItems(ctx, db, dialect, head.CgSQL)
	}
	validField := validFieldOf(items)
	if !validField(dim) {
		return nil, errors.New("维度字段[" + dim + "]不在报表字段中")
	}
	aggFn, ok := chartAggs[strings.ToLower(agg)]
	if !ok {
		aggFn = "SUM"
	}
	if strings.EqualFold(aggFn, "COUNT") || measure == "" {
		measure, aggFn = "*", "COUNT"
	} else if !validField(measure) {
		return nil, errors.New("度量字段[" + measure + "]不在报表字段中")
	}
	defaults := map[string]string{}
	for _, p := range params {
		defaults[p.ParamName] = p.ParamValue
	}
	inner, args := substituteParams(dialect, base, defaults, q)
	dimCol := dbx.Quote(dialect, dim)
	valExpr := aggFn + "(" + measure + ")"
	if measure == "*" {
		valExpr = "COUNT(*)"
	}
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	grouped := fmt.Sprintf("SELECT %s AS litrpt_dim, %s AS litrpt_val FROM ( %s ) litrpt_chart GROUP BY %s ORDER BY litrpt_val DESC",
		dimCol, valExpr, inner, dimCol)
	sqlText := dbx.LimitWrapN(dialect, grouped, limit)
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, sqlText), args...)
	if err != nil {
		return nil, fmt.Errorf("图表数据查询失败: %w", err)
	}
	defer rows.Close()
	out := []ChartPoint{}
	for rows.Next() {
		var name, val any
		if err := rows.Scan(&name, &val); err != nil {
			return nil, err
		}
		out = append(out, ChartPoint{Name: fmtCell(normalizeVal(name)), Value: normalizeVal(val)})
	}
	return out, rows.Err()
}

// ───── 表单建表 ─────

// FormField 表单字段定义。
type FormField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"` // text | number | date
	Required bool   `json:"required"`
}

var formTypeDDL = map[string]string{
	"text":   "VARCHAR(255)",
	"number": "NUMERIC(18,4)",
	"date":   "TIMESTAMP",
}

// CreateFormTable 在指定库上创建表单数据表。
func CreateFormTable(ctx context.Context, db *sql.DB, dialect, tableName string, fields []FormField) error {
	if !dbx.ValidIdent(tableName) {
		return errors.New("表名不合法")
	}
	if len(fields) == 0 || len(fields) > 60 {
		return errors.New("字段数量须在 1~60 之间")
	}
	cols := []string{"id VARCHAR(36) PRIMARY KEY"}
	for _, f := range fields {
		if !identRe.MatchString(f.Name) {
			return fmt.Errorf("字段名[%s]不合法", f.Name)
		}
		tp, ok := formTypeDDL[f.Type]
		if !ok {
			tp = formTypeDDL["text"]
		}
		def := dbx.Quote(dialect, f.Name) + " " + tp
		if f.Required {
			def += " NOT NULL"
		}
		cols = append(cols, def)
	}
	ddl := "CREATE TABLE " + dbx.Quote(dialect, tableName) + " (" + strings.Join(cols, ", ") + ")"
	q, ok := dbx.DDLStatement(ddl, dialect)
	if !ok {
		return errors.New("当前方言不支持建表")
	}
	if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, q)); err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}
	return nil
}

// ───── AI 生成 SQL（OpenAI 兼容接口）─────

var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\s*(.+?)```")

// ChatMessage AI 对话消息。
type ChatMessage struct {
	Role    string `json:"role"` // system / user / assistant
	Content string `json:"content"`
}

// aiClient 全局共享的 AI HTTP 客户端（复用连接池）。
var aiClient = &http.Client{Timeout: 120 * time.Second}

// chatCompletion 调用 OpenAI 兼容接口的 chat/completions，返回首条回复文本。
func chatCompletion(ctx context.Context, aiCfg AiConfig, temperature float32, messages []ChatMessage) (string, error) {
	reqBody := map[string]any{
		"model":       aiCfg.Model,
		"messages":    messages,
		"temperature": temperature,
	}
	b, _ := json.Marshal(reqBody)
	url := strings.TrimRight(aiCfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiCfg.APIKey)
	resp, err := aiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用AI失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("AI接口返回 %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return "", fmt.Errorf("AI响应解析失败: %w", err)
	}
	if len(rr.Choices) == 0 {
		msg := "AI未返回结果"
		if rr.Error.Message != "" {
			msg += ": " + rr.Error.Message
		}
		return "", errors.New(msg)
	}
	return rr.Choices[0].Message.Content, nil
}

// AIChatStream 流式调用 chat/completions（stream:true），每个增量片段回调 onDelta；
// 返回完整回复文本。上游非 200 或解析失败返回错误（已输出的增量由调用方处理）。
func AIChatStream(ctx context.Context, aiCfg AiConfig, history []ChatMessage, onDelta func(string)) (string, error) {
	if aiCfg.APIKey == "" || aiCfg.BaseURL == "" {
		return "", errors.New("未配置AI：请在 config.yaml 的 ai 段填写 base_url / api_key / model")
	}
	msgs := make([]ChatMessage, 0, len(history)+1)
	msgs = append(msgs, ChatMessage{Role: "system", Content: "你是 LiteReport 低代码报表平台的AI助手，熟悉 SQL、数据分析与后端开发。" +
		"回答使用中文，简洁准确；给出 SQL 时使用 ```sql 代码块。"})
	msgs = append(msgs, history...)
	reqBody := map[string]any{
		"model":       aiCfg.Model,
		"messages":    msgs,
		"temperature": 0.5,
		"stream":      true,
	}
	b, _ := json.Marshal(reqBody)
	url := strings.TrimRight(aiCfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiCfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := aiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用AI失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("AI接口返回 %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 跳过无法解析的心跳/注释行
		}
		if chunk.Error.Message != "" {
			return full.String(), fmt.Errorf("AI返回错误: %s", chunk.Error.Message)
		}
		if delta := chunk.Choices[0].Delta.Content; delta != "" && len(chunk.Choices) > 0 {
			full.WriteString(delta)
			onDelta(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), err
	}
	if full.Len() == 0 {
		return "", errors.New("AI未返回内容")
	}
	return full.String(), nil
}

// AIChat AI助手多轮对话（页面 /ai.html）。
func AIChat(ctx context.Context, aiCfg AiConfig, history []ChatMessage) (string, error) {
	if aiCfg.APIKey == "" || aiCfg.BaseURL == "" {
		return "", errors.New("未配置AI：请在 config.yaml 的 ai 段填写 base_url / api_key / model")
	}
	msgs := make([]ChatMessage, 0, len(history)+1)
	msgs = append(msgs, ChatMessage{Role: "system", Content: "你是 LiteReport 低代码报表平台的AI助手，熟悉 SQL、数据分析与后端开发。" +
		"回答使用中文，简洁准确；给出 SQL 时使用 ```sql 代码块。"})
	msgs = append(msgs, history...)
	return chatCompletion(ctx, aiCfg, 0.5, msgs)
}

// GenerateSQL 调用 OpenAI 兼容接口，依据表结构与自然语言描述生成 SELECT。
func GenerateSQL(ctx context.Context, aiCfg AiConfig, desc, table string, fields []Field) (string, error) {
	if aiCfg.APIKey == "" || aiCfg.BaseURL == "" {
		return "", errors.New("未配置AI：请在 config.yaml 的 ai 段填写 base_url / api_key / model")
	}
	var sb strings.Builder
	sb.WriteString("以下是数据表 ")
	sb.WriteString(table)
	sb.WriteString(" 的字段：\n")
	for _, f := range fields {
		sb.WriteString("- " + f.Name + " (" + f.Type + ")\n")
	}
	sb.WriteString("\n请根据需求生成一条查询SQL，只输出SQL本身（不要解释、不要分号）：\n需求：")
	sb.WriteString(desc)
	messages := []ChatMessage{
		{Role: "system", Content: "你是SQL专家，只输出一条可直接执行的SELECT语句。"},
		{Role: "user", Content: sb.String()},
	}
	content, err := chatCompletion(ctx, aiCfg, 0.2, messages)
	if err != nil {
		return "", err
	}
	// 提取 ```sql 围栏内容，否则取整个内容
	if m := fenceRe.FindStringSubmatch(content); m != nil {
		content = m[1]
	}
	sqlText := strings.TrimSpace(content)
	if i := strings.LastIndex(sqlText, ";"); i >= 0 {
		sqlText = sqlText[:i]
	}
	if _, err := CleanSQL(sqlText); err != nil {
		return "", fmt.Errorf("AI生成的SQL未通过校验: %v\n%s", err, content)
	}
	return sqlText, nil
}

// ───── 定时报表推送 ─────

// SmtpConfig 邮件服务配置。
type SmtpConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	AlertEmail string // 推送失败告警收件箱
}

// dialSMTP 建立STARTTLS SMTP连接并完成认证。
func dialSMTP(cfg SmtpConfig) (*smtp.Client, error) {
	conn, err := smtp.Dial(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	if err != nil {
		return nil, err
	}
	if ok, _ := conn.Extension("STARTTLS"); ok {
		if err := conn.StartTLS(nil); err != nil {
			conn.Close()
			return nil, fmt.Errorf("STARTTLS失败: %w", err)
		}
	}
	if err = conn.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP认证失败: %w", err)
	}
	return conn, nil
}

// SendAlertMail 推送失败告警（纯文本；节流由调用方控制）。
func SendAlertMail(cfg SmtpConfig, to, subject, body string) error {
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		cfg.From, to, subject, strings.ReplaceAll(body, "\n", "\r\n"))
	conn, err := dialSMTP(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.Mail(cfg.From); err != nil {
		return err
	}
	if err = conn.Rcpt(to); err != nil {
		return err
	}
	w, err := conn.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write([]byte(msg)); err != nil {
		return err
	}
	return w.Close()
}

// StartPushScheduler 每分钟扫描到期推送任务：按报表配置的数据源生成 Excel 并发送
// （邮件或钉钉/企微机器人）。SMTP 未配置且渠道为邮件时仅记录日志跳过。
// 返回 stop 函数：优雅停机时调用，等待进行中的一轮扫描结束。
func StartPushScheduler(mgr *dbx.Manager, smtpCfg SmtpConfig) (stop func()) {
	metaDB, dialect := mgr.Meta()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				due, err := store.PushDue(ctx, metaDB, dialect, time.Now().Format("2006-01-02 15:04:05"))
				if err != nil {
					slog.Error("推送扫描失败", "err", err)
					cancel()
					continue
				}
				for _, p := range due {
					select {
					case <-done:
						cancel()
						return
					default:
					}
					if err := runOnePush(ctx, mgr, smtpCfg, p); err != nil {
						slog.Error("推送发送失败", "report", p.ReportCode, "err", err)
						alertPushFailure(smtpCfg, p, err.Error())
						_ = store.PushMarkRun(ctx, metaDB, dialect, p.ID, false, err.Error())
					} else {
						slog.Info("推送已发送", "report", p.ReportCode, "to", p.Emails)
						_ = store.PushMarkRun(ctx, metaDB, dialect, p.ID, true, "")
					}
				}
				cancel()
			}
		}
	}()
	return func() {
		close(done)
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
		}
	}
}

var (
	alertMu     sync.Mutex
	lastAlertAt = map[string]time.Time{}
)

// alertPushFailure 推送失败邮件告警（同一任务 1 小时至多一封，防轰炸）。
func alertPushFailure(smtpCfg SmtpConfig, p store.PushRow, errText string) {
	if smtpCfg.AlertEmail == "" || smtpCfg.Host == "" {
		return
	}
	alertMu.Lock()
	if t, ok := lastAlertAt[p.ID]; ok && time.Since(t) < time.Hour {
		alertMu.Unlock()
		return
	}
	lastAlertAt[p.ID] = time.Now()
	alertMu.Unlock()
	subject := "LiteReport 推送失败告警 - " + p.ReportCode
	body := fmt.Sprintf("报表[%s] 定时推送失败（收件人: %s）：\r\n%s\r\n\r\n请检查 SMTP 配置、收件邮箱或报表数据源。", p.ReportCode, p.Emails, errText)
	if e := SendAlertMail(smtpCfg, smtpCfg.AlertEmail, subject, body); e != nil {
		slog.Error("推送失败告警邮件发送失败", "report", p.ReportCode, "err", e)
	}
}

func runOnePush(ctx context.Context, mgr *dbx.Manager, smtpCfg SmtpConfig, p store.PushRow) error {
	metaDB, metaDialect := mgr.Meta()
	head, err := store.HeadFindByKey(ctx, metaDB, metaDialect, p.ReportCode)
	if err != nil {
		return err
	}
	items, _ := store.ItemsByHead(ctx, metaDB, metaDialect, head.ID)
	params, _ := store.ParamsByHead(ctx, metaDB, metaDialect, head.ID)
	target, tdialect, err := mgr.Get(head.DbSource)
	if err != nil {
		return fmt.Errorf("解析报表数据源失败: %w", err)
	}

	// webhook 渠道（钉钉/企微机器人）：推 markdown 卡片
	if ch := strings.ToLower(p.Channel); ch == "dingtalk" || ch == "wecom" {
		_, rowCount, err := ExportExcelBytes(ctx, target, tdialect, head, items, params, map[string][]string{})
		if err != nil {
			return err
		}
		md := fmt.Sprintf("### LiteReport 定时报表\n- **报表**: %s\n- **时间**: %s\n- **数据规模**: %d 行\n\n> Excel 已生成，请到平台下载完整数据。",
			head.ReportName, time.Now().Format("2006-01-02 15:04"), rowCount)
		return SendWebhook(ctx, ch, p.Webhook, "LiteReport 定时报表 - "+head.ReportName, md)
	}

	if smtpCfg.Host == "" {
		return errors.New("SMTP未配置（config.yaml smtp 段）")
	}
	data, _, err := ExportExcelBytes(ctx, target, tdialect, head, items, params, map[string][]string{})
	if err != nil {
		return err
	}
	return SendReportMail(smtpCfg, p.Emails, "LiteReport 定时报表 - "+head.ReportName, "请查收附件报表数据。", head.ReportName+".xlsx", data)
}

// SendReportMail 发送带 xlsx 附件的邮件（net/smtp + STARTTLS）。
func SendReportMail(cfg SmtpConfig, to, subject, body, fileName string, attach []byte) error {
	if cfg.Host == "" || to == "" {
		return errors.New("SMTP未配置或收件人为空")
	}
	boundary := "litereport" + fmt.Sprintf("%d", time.Now().UnixNano())
	hdr := map[string]string{}
	hdr["From"] = cfg.From
	hdr["To"] = to
	hdr["Subject"] = subject
	hdr["MIME-Version"] = "1.0"
	var buf bytes.Buffer
	for k, v := range hdr {
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, body)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: application/octet-stream; name=\"%s\"\r\n", boundary, fileName)
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", fileName)
	enc := base64Lines(attach)
	buf.WriteString(enc)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)

	conn, err := dialSMTP(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.Mail(cfg.From); err != nil {
		return err
	}
	for _, rcpt := range strings.Split(to, ",") {
		if rcpt = strings.TrimSpace(rcpt); rcpt != "" {
			if err = conn.Rcpt(rcpt); err != nil {
				return err
			}
		}
	}
	w, err := conn.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(buf.Bytes()); err != nil {
		return err
	}
	return w.Close()
}

// AiConfig AI 接口配置。
type AiConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// base64Lines 输出 76 列折行的 base64（邮件附件编码）。
func base64Lines(data []byte) string {
	const raw = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	enc := base64.StdEncoding
	src := []byte(data)
	n := len(src)
	line := 57 // 57字节 → 76字符
	for i := 0; i < n; i += line {
		end := i + line
		if end > n {
			end = n
		}
		sb.WriteString(enc.EncodeToString(src[i:end]))
		sb.WriteString("\r\n")
	}
	return sb.String()
}

// isoTRe 匹配 ISO 日期时间中间的 T（如 2026-01-02T15:04:05），仅对完整日期前缀生效，
// 避免把 "Top10" 之类普通字符串里的 T 误替换。
var isoTRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[Tt](\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)$`)

// fmtCell 服务端单元格格式化（与前端展示规则一致）。
func fmtCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if m := isoTRe.FindStringSubmatch(t); m != nil {
			return m[1] + " " + m[2]
		}
		return t
	case json.Number:
		return string(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}
