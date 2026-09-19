// 扩展功能的元数据表与存储：数据源管理、字典、图表、配置版本、审计日志、表单、定时推送。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"litereport/internal/dbx"
)

// EnsureExtraTables 幂等创建扩展功能表。
func EnsureExtraTables(ctx context.Context, db *sql.DB, dialect string) error {
	stmts := []string{
		`CREATE TABLE onl_datasource (
			dkey VARCHAR(64) PRIMARY KEY,
			dname VARCHAR(200),
			dtype VARCHAR(32),
			dsn TEXT,
			create_time TIMESTAMP
		)`,
		`CREATE TABLE sys_dict (
			id VARCHAR(36) PRIMARY KEY,
			dict_code VARCHAR(100),
			label VARCHAR(200),
			value VARCHAR(200),
			order_num INT
		)`,
		`CREATE TABLE onl_chart (
			id VARCHAR(36) PRIMARY KEY,
			chart_name VARCHAR(200),
			report_code VARCHAR(100),
			chart_type VARCHAR(20),
			dim_field VARCHAR(100),
			measure_field VARCHAR(100),
			agg VARCHAR(20),
			create_time TIMESTAMP
		)`,
		`CREATE TABLE onl_cgreport_version (
			id VARCHAR(36) PRIMARY KEY,
			head_id VARCHAR(36),
			snapshot TEXT,
			create_by VARCHAR(64),
			create_time TIMESTAMP
		)`,
		`CREATE TABLE onl_audit_log (
			id VARCHAR(36) PRIMARY KEY,
			log_time TIMESTAMP,
			user_name VARCHAR(64),
			ip VARCHAR(64),
			action VARCHAR(50),
			target VARCHAR(200),
			detail TEXT
		)`,
		`CREATE TABLE onl_form (
			id VARCHAR(36) PRIMARY KEY,
			form_name VARCHAR(200),
			table_name VARCHAR(100),
			fields_json TEXT,
			create_by VARCHAR(64),
			create_time TIMESTAMP
		)`,
		`CREATE TABLE onl_report_push (
			id VARCHAR(36) PRIMARY KEY,
			report_code VARCHAR(100),
			emails TEXT,
			interval_minutes INT,
			enabled VARCHAR(2) DEFAULT 'Y',
			channel VARCHAR(20) DEFAULT 'email',
			webhook TEXT,
			last_run TIMESTAMP,
			last_status VARCHAR(20),
			last_error VARCHAR(500),
			create_time TIMESTAMP
		)`,
		`CREATE INDEX idx_audit_time ON onl_audit_log(log_time)`,
		`CREATE INDEX idx_version_head ON onl_cgreport_version(head_id)`,
		`CREATE INDEX idx_dict_code ON sys_dict(dict_code)`,
	}
	for _, s := range stmts {
		q, ok := dbx.DDLStatement(s, dialect)
		if !ok {
			continue
		}
		if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, q)); err != nil {
			return err
		}
	}
	return nil
}

// ───── 数据源管理 ─────

type DSRow struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
	DSN  string `json:"dsn"` // 存储层返回原文（可能加密串，由上层解密）
}

func DSList(ctx context.Context, db *sql.DB, dialect string) ([]DSRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT dkey, dname, dtype, dsn FROM onl_datasource ORDER BY dkey`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DSRow{}
	for rows.Next() {
		var r DSRow
		if err := rows.Scan(&r.Key, &r.Name, &r.Type, &r.DSN); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func DSSave(ctx context.Context, db *sql.DB, dialect string, r DSRow) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM onl_datasource WHERE dkey=?`), r.Key); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO onl_datasource (dkey, dname, dtype, dsn, create_time) VALUES (?,?,?,?,?)`),
		r.Key, r.Name, r.Type, r.DSN, timeNow())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func DSDelete(ctx context.Context, db *sql.DB, dialect, key string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM onl_datasource WHERE dkey=?`), key)
	return err
}

// ───── 字典 ─────

type DictItem struct {
	ID       string `json:"id"`
	DictCode string `json:"dictCode"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	OrderNum int    `json:"orderNum"`
}

func DictList(ctx context.Context, db *sql.DB, dialect, dictCode string) ([]DictItem, error) {
	q := `SELECT id, dict_code, label, value, order_num FROM sys_dict`
	var args []any
	if dictCode != "" {
		q += ` WHERE dict_code=?`
		args = append(args, dictCode)
	}
	q += ` ORDER BY dict_code, order_num`
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DictItem{}
	for rows.Next() {
		var d DictItem
		if err := rows.Scan(&d.ID, &d.DictCode, &d.Label, &d.Value, &d.OrderNum); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func DictSave(ctx context.Context, db *sql.DB, dialect string, d DictItem) error {
	if d.ID == "" {
		d.ID = NewID()
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO sys_dict (id, dict_code, label, value, order_num) VALUES (?,?,?,?,?)`),
		d.ID, d.DictCode, d.Label, d.Value, d.OrderNum)
	return err
}

func DictDelete(ctx context.Context, db *sql.DB, dialect, id string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM sys_dict WHERE id=?`), id)
	return err
}

// ───── 图表 ─────

type ChartRow struct {
	ID           string `json:"id"`
	ChartName    string `json:"chartName"`
	ReportCode   string `json:"reportCode"`
	ChartType    string `json:"chartType"` // bar/line/pie
	DimField     string `json:"dimField"`
	MeasureField string `json:"measureField"`
	Agg          string `json:"agg"` // sum/count/avg/max/min
	CreateTime   string `json:"createTime"`
}

func ChartList(ctx context.Context, db *sql.DB, dialect string) ([]ChartRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, chart_name, report_code, chart_type, dim_field, measure_field, agg, create_time FROM onl_chart ORDER BY create_time DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChartRow{}
	for rows.Next() {
		var c ChartRow
		var ct sql.NullTime
		if err := rows.Scan(&c.ID, &c.ChartName, &c.ReportCode, &c.ChartType, &c.DimField, &c.MeasureField, &c.Agg, &ct); err != nil {
			return nil, err
		}
		c.CreateTime = FmtNullTime(ct)
		out = append(out, c)
	}
	return out, rows.Err()
}

func ChartSave(ctx context.Context, db *sql.DB, dialect string, c *ChartRow) error {
	if c.ID == "" {
		c.ID = NewID()
		_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_chart (id, chart_name, report_code, chart_type, dim_field, measure_field, agg, create_time) VALUES (?,?,?,?,?,?,?,?)`),
			c.ID, c.ChartName, c.ReportCode, c.ChartType, c.DimField, c.MeasureField, c.Agg, timeNow())
		return err
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE onl_chart SET chart_name=?, report_code=?, chart_type=?, dim_field=?, measure_field=?, agg=? WHERE id=?`),
		c.ChartName, c.ReportCode, c.ChartType, c.DimField, c.MeasureField, c.Agg, c.ID)
	return err
}

func ChartGet(ctx context.Context, db *sql.DB, dialect, id string) (*ChartRow, error) {
	var c ChartRow
	var ct sql.NullTime
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT id, chart_name, report_code, chart_type, dim_field, measure_field, agg, create_time FROM onl_chart WHERE id=?`), id).
		Scan(&c.ID, &c.ChartName, &c.ReportCode, &c.ChartType, &c.DimField, &c.MeasureField, &c.Agg, &ct)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	c.CreateTime = FmtNullTime(ct)
	return &c, err
}

func ChartDelete(ctx context.Context, db *sql.DB, dialect, id string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM onl_chart WHERE id=?`), id)
	return err
}

// ───── 配置版本 ─────

type VersionRow struct {
	ID         string `json:"id"`
	HeadID     string `json:"headId"`
	CreateBy   string `json:"createBy"`
	CreateTime string `json:"createTime"`
	Snapshot   string `json:"-"`
}

func VersionInsert(ctx context.Context, db *sql.DB, dialect, headID, snapshot, user string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO onl_cgreport_version (id, head_id, snapshot, create_by, create_time) VALUES (?,?,?,?,?)`),
		NewID(), headID, snapshot, user, timeNow())
	return err
}

func VersionList(ctx context.Context, db *sql.DB, dialect, headID string) ([]VersionRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, head_id, create_by, create_time FROM onl_cgreport_version WHERE head_id=? ORDER BY create_time DESC`), headID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionRow{}
	for rows.Next() {
		var v VersionRow
		var ct sql.NullTime
		if err := rows.Scan(&v.ID, &v.HeadID, &v.CreateBy, &ct); err != nil {
			return nil, err
		}
		v.CreateTime = FmtNullTime(ct)
		out = append(out, v)
	}
	return out, rows.Err()
}

func VersionGet(ctx context.Context, db *sql.DB, dialect, id string) (string, error) {
	var snap string
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT snapshot FROM onl_cgreport_version WHERE id=?`), id).Scan(&snap)
	return snap, err
}

// ───── 审计日志 ─────

type AuditRow struct {
	ID       string `json:"id"`
	LogTime  string `json:"logTime"`
	UserName string `json:"userName"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
}

func AuditInsert(ctx context.Context, db *sql.DB, dialect string, a AuditRow) error {
	if a.ID == "" {
		a.ID = NewID()
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO onl_audit_log (id, log_time, user_name, ip, action, target, detail) VALUES (?,?,?,?,?,?,?)`),
		a.ID, timeNow(), a.UserName, a.IP, a.Action, a.Target, a.Detail)
	return err
}

func AuditList(ctx context.Context, db *sql.DB, dialect, action, user string, page, size int) ([]AuditRow, int, error) {
	where := " WHERE 1=1"
	var args []any
	if action != "" {
		where += " AND action=?"
		args = append(args, action)
	}
	if user != "" {
		where += " AND user_name LIKE ?"
		args = append(args, "%"+user+"%")
	}
	var total int
	if err := db.QueryRowContext(ctx, dbx.Rebind(dialect, "SELECT COUNT(*) FROM onl_audit_log"+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := dbx.PageWrap(dialect, "SELECT id, log_time, user_name, ip, action, target, detail FROM onl_audit_log"+where+" ORDER BY log_time DESC", page, size)
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var a AuditRow
		var lt sql.NullTime
		if err := rows.Scan(&a.ID, &lt, &a.UserName, &a.IP, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, 0, err
		}
		a.LogTime = FmtNullTime(lt)
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// ───── 表单 ─────

type FormRow struct {
	ID         string `json:"id"`
	FormName   string `json:"formName"`
	TableName  string `json:"tableName"`
	FieldsJSON string `json:"fieldsJson"`
	CreateBy   string `json:"createBy"`
	CreateTime string `json:"createTime"`
}

func FormList(ctx context.Context, db *sql.DB, dialect string) ([]FormRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, form_name, table_name, fields_json, create_by, create_time FROM onl_form ORDER BY create_time DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FormRow{}
	for rows.Next() {
		var f FormRow
		var ct sql.NullTime
		if err := rows.Scan(&f.ID, &f.FormName, &f.TableName, &f.FieldsJSON, &f.CreateBy, &ct); err != nil {
			return nil, err
		}
		f.CreateTime = FmtNullTime(ct)
		out = append(out, f)
	}
	return out, rows.Err()
}

func FormSave(ctx context.Context, db *sql.DB, dialect string, f *FormRow) error {
	if f.ID == "" {
		f.ID = NewID()
		_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_form (id, form_name, table_name, fields_json, create_by, create_time) VALUES (?,?,?,?,?,?)`),
			f.ID, f.FormName, f.TableName, f.FieldsJSON, f.CreateBy, timeNow())
		return err
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE onl_form SET form_name=?, table_name=?, fields_json=? WHERE id=?`),
		f.FormName, f.TableName, f.FieldsJSON, f.ID)
	return err
}

func FormGet(ctx context.Context, db *sql.DB, dialect, id string) (*FormRow, error) {
	var f FormRow
	var ct sql.NullTime
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT id, form_name, table_name, fields_json, create_by, create_time FROM onl_form WHERE id=?`), id).
		Scan(&f.ID, &f.FormName, &f.TableName, &f.FieldsJSON, &f.CreateBy, &ct)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	f.CreateTime = FmtNullTime(ct)
	return &f, err
}

func FormDelete(ctx context.Context, db *sql.DB, dialect, id string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM onl_form WHERE id=?`), id)
	return err
}

// ───── 定时推送 ─────

type PushRow struct {
	ID              string `json:"id"`
	ReportCode      string `json:"reportCode"`
	Emails          string `json:"emails"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Enabled         string `json:"enabled"`
	Channel         string `json:"channel"` // email / dingtalk / wecom
	Webhook         string `json:"webhook"`
	LastRun         string `json:"lastRun"`
	LastStatus      string `json:"lastStatus"` // ok / fail
	LastError       string `json:"lastError"`
	CreateTime      string `json:"createTime"`
}

func PushList(ctx context.Context, db *sql.DB, dialect string) ([]PushRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, report_code, emails, interval_minutes, enabled, channel, webhook, last_run, last_status, last_error, create_time FROM onl_report_push ORDER BY create_time DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushRow{}
	for rows.Next() {
		var p PushRow
		var lr, ct sql.NullTime
		var ls, le sql.NullString
		if err := rows.Scan(&p.ID, &p.ReportCode, &p.Emails, &p.IntervalMinutes, &p.Enabled, &p.Channel, &p.Webhook, &lr, &ls, &le, &ct); err != nil {
			return nil, err
		}
		p.LastRun = FmtNullTime(lr)
		p.LastStatus = ls.String
		p.LastError = le.String
		p.CreateTime = FmtNullTime(ct)
		out = append(out, p)
	}
	return out, rows.Err()
}

func PushSave(ctx context.Context, db *sql.DB, dialect string, p *PushRow) error {
	if p.Channel == "" {
		p.Channel = "email"
	}
	if p.ID == "" {
		p.ID = NewID()
		if p.Enabled == "" {
			p.Enabled = "Y"
		}
		_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_report_push (id, report_code, emails, interval_minutes, enabled, channel, webhook, create_time) VALUES (?,?,?,?,?,?,?,?)`),
			p.ID, p.ReportCode, p.Emails, p.IntervalMinutes, p.Enabled, p.Channel, p.Webhook, timeNow())
		return err
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE onl_report_push SET report_code=?, emails=?, interval_minutes=?, enabled=?, channel=?, webhook=? WHERE id=?`),
		p.ReportCode, p.Emails, p.IntervalMinutes, p.Enabled, p.Channel, p.Webhook, p.ID)
	return err
}

func PushDelete(ctx context.Context, db *sql.DB, dialect, id string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM onl_report_push WHERE id=?`), id)
	return err
}

// PushDue 列出到期任务（last_run 由 PushMarkRun 在发送后回写，发送结果同时记录 last_status/last_error）。
func PushDue(ctx context.Context, db *sql.DB, dialect string, now string) ([]PushRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, `SELECT id, report_code, emails, interval_minutes, channel, webhook, last_run FROM onl_report_push WHERE enabled='Y'`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	due := []PushRow{}
	for rows.Next() {
		var p PushRow
		var lr sql.NullTime
		if err := rows.Scan(&p.ID, &p.ReportCode, &p.Emails, &p.IntervalMinutes, &p.Channel, &p.Webhook, &lr); err != nil {
			return nil, err
		}
		dueAt := ""
		if lr.Valid {
			dueAt = lr.Time.Add(time.Duration(p.IntervalMinutes) * time.Minute).Format("2006-01-02 15:04:05")
		}
		if !lr.Valid || dueAt <= now {
			due = append(due, p)
		}
	}
	return due, rows.Err()
}

// PushMarkRun 回写推送执行结果：last_run、last_status（ok/fail）与 last_error（截断500字）。
func PushMarkRun(ctx context.Context, db *sql.DB, dialect, id string, ok bool, errMsg string) error {
	status, errText := "ok", ""
	if !ok {
		status, errText = "fail", errMsg
	}
	if len(errText) > 500 {
		errText = errText[:500]
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE onl_report_push SET last_run=?, last_status=?, last_error=? WHERE id=?`),
		time.Now(), status, errText, id)
	return err
}

func FmtNullTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02 15:04:05")
}

// CleanupRetention 按保留天数清理审计日志 / SQL执行历史 / 配置版本快照
// （days<=0 表示该项永久保留）。表名与列名均为代码内常量，无注入面。
func CleanupRetention(ctx context.Context, db *sql.DB, dialect string, auditDays, sqlHistoryDays, versionDays, chatDays int) error {
	run := func(table, col string, days int) error {
		if days <= 0 {
			return nil
		}
		cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, dbx.Rebind(dialect,
			"DELETE FROM "+table+" WHERE "+col+" < ?"), cutoff); err != nil {
			return fmt.Errorf("清理%s失败: %w", table, err)
		}
		return nil
	}
	if err := run("onl_audit_log", "log_time", auditDays); err != nil {
		return err
	}
	if err := run("onl_sql_history", "create_time", sqlHistoryDays); err != nil {
		return err
	}
	if err := run("onl_cgreport_version", "create_time", versionDays); err != nil {
		return err
	}
	return run("ai_chat_history", "create_time", chatDays)
}

func timeNow() any { return time.Now() }
