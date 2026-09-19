// schema 迁移机制：platform_schema 记录版本，按序应用增量变更。
// 增量以"加列"为主；重复执行安全（列已存在的报错会被忽略）。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"litereport/internal/dbx"
)

// Migration 一次 schema 增量：Statements 按方言生成待执行 SQL。
type Migration struct {
	Ver        int
	Name       string
	Statements func(dialect string) []string
}

// addColumn 通用列定义 → 各方言 ALTER 语句。
func addColumn(dialect, table, col, genericType string) []string {
	tp := dbx.TranslateDDL("X "+genericType, dialect)[2:] // 复用类型翻译
	switch dialect {
	case dbx.Oracle:
		return []string{fmt.Sprintf("ALTER TABLE %s ADD %s %s", table, col, tp)}
	default:
		return []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, tp)}
	}
}

// Migrations 全部增量迁移（按 Ver 升序执行）。
var Migrations = []Migration{
	{Ver: 1, Name: "报表头增加行级数据权限字段", Statements: func(d string) []string {
		return append(addColumn(d, "onl_cgreport_head", "perm_filter", "TEXT"),
			addColumn(d, "onl_cgreport_head", "perm_enabled", "VARCHAR(2)")...)
	}},
	{Ver: 2, Name: "字段明细增加渲染规则", Statements: func(d string) []string {
		return addColumn(d, "onl_cgreport_item", "render_rule", "VARCHAR(500)")
	}},
	{Ver: 3, Name: "平台用户表（支持改密与会话吊销）", Statements: func(d string) []string {
		return []string{dbx.TranslateDDL(`CREATE TABLE platform_user (
			uname VARCHAR(64) PRIMARY KEY,
			pwd_hash VARCHAR(100),
			token_version INT DEFAULT 0
		)`, d)}
	}},
	{Ver: 4, Name: "推送表增加webhook渠道", Statements: func(d string) []string {
		return append(addColumn(d, "onl_report_push", "channel", "VARCHAR(20)"),
			addColumn(d, "onl_report_push", "webhook", "TEXT")...)
	}},
	{Ver: 5, Name: "平台用户表增加角色", Statements: func(d string) []string {
		return addColumn(d, "platform_user", "role", "VARCHAR(20)")
	}},
	{Ver: 6, Name: "SQL执行历史表", Statements: func(d string) []string {
		return []string{dbx.TranslateDDL(`CREATE TABLE onl_sql_history (
			id VARCHAR(36) PRIMARY KEY,
			user_name VARCHAR(64),
			db_source VARCHAR(64),
			sql_text TEXT,
			duration_ms INT,
			row_count INT,
			ok VARCHAR(2),
			err_msg VARCHAR(500),
			create_time TIMESTAMP
		)`, d)}
	}},
	{Ver: 7, Name: "推送表增加执行结果字段", Statements: func(d string) []string {
		return append(addColumn(d, "onl_report_push", "last_status", "VARCHAR(20)"),
			addColumn(d, "onl_report_push", "last_error", "VARCHAR(500)")...)
	}},
	{Ver: 8, Name: "AI对话历史与AI配置表", Statements: func(d string) []string {
		return []string{
			dbx.TranslateDDL(`CREATE TABLE ai_chat_history (
				id VARCHAR(36) PRIMARY KEY,
				user_name VARCHAR(64),
				role VARCHAR(20),
				content TEXT,
				create_time TIMESTAMP
			)`, d),
			dbx.TranslateDDL(`CREATE TABLE ai_config (
				id INT PRIMARY KEY,
				base_url VARCHAR(500),
				api_key_enc TEXT,
				model VARCHAR(100),
				update_by VARCHAR(64),
				update_time TIMESTAMP
			)`, d),
		}
	}},
	{Ver: 9, Name: "AI对话历史增加seq排序字段", Statements: func(d string) []string {
		return addColumn(d, "ai_chat_history", "seq", "INT")
	}},
	{Ver: 10, Name: "报表头增加分类字段", Statements: func(d string) []string {
		return addColumn(d, "onl_cgreport_head", "category", "VARCHAR(100)")
	}},
	{Ver: 11, Name: "报表头增加分享链接字段", Statements: func(d string) []string {
		return append(addColumn(d, "onl_cgreport_head", "share_token", "VARCHAR(64)"),
			addColumn(d, "onl_cgreport_head", "share_expire", "TIMESTAMP")...)
	}},
	{Ver: 12, Name: "AI对话历史增加会话维度", Statements: func(d string) []string {
		stmts := append(addColumn(d, "ai_chat_history", "session_id", "VARCHAR(36)"),
			"UPDATE ai_chat_history SET session_id='legacy' WHERE session_id IS NULL OR session_id=''")
		if idx, ok := dbx.DDLStatement("CREATE INDEX idx_chat_session ON ai_chat_history(user_name, session_id)", d); ok {
			stmts = append(stmts, idx)
		}
		return stmts
	}},
}

// RunMigrations 执行全部未应用的迁移。
func RunMigrations(ctx context.Context, db *sql.DB, dialect string) error {
	verTbl := dbx.TranslateDDL(`CREATE TABLE platform_schema (
		version INT PRIMARY KEY,
		name VARCHAR(200),
		applied_at TIMESTAMP
	)`, dialect)
	q, ok := dbx.DDLStatement(verTbl, dialect)
	if !ok {
		return nil
	}
	if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, q)); err != nil {
		return fmt.Errorf("创建版本表失败: %w", err)
	}
	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, `SELECT version FROM platform_schema`))
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err == nil {
			applied[v] = true
		}
	}
	rows.Close()
	for _, m := range Migrations {
		if applied[m.Ver] {
			continue
		}
		for _, s := range m.Statements(dialect) {
			if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, s)); err != nil {
				// 列已存在等幂等冲突直接忽略
				msg := strings.ToLower(err.Error())
				if strings.Contains(msg, "duplicate") || strings.Contains(msg, "exists") ||
					strings.Contains(msg, "already") || strings.Contains(msg, "-1430") ||
					strings.Contains(msg, "-1060") || strings.Contains(msg, "42710") {
					continue
				}
				return fmt.Errorf("迁移v%d[%s]失败: %w", m.Ver, m.Name, err)
			}
		}
		_, _ = db.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO platform_schema (version, name, applied_at) VALUES (?,?,?)`),
			m.Ver, m.Name, time.Now())
	}
	return nil
}

// UserTokenVersion 查询用户的会话版本（DB 用户不存在返回 0）。
func UserTokenVersion(ctx context.Context, db *sql.DB, dialect, name string) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT token_version FROM platform_user WHERE uname=?`), name).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

// UserPasswordHash 查询 DB 用户的密码哈希（平台用户表优先于 config.yaml）。
func UserPasswordHash(ctx context.Context, db *sql.DB, dialect, name string) (string, error) {
	var h string
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT pwd_hash FROM platform_user WHERE uname=?`), name).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

// UserSetPassword 新增/更新用户密码哈希并递增会话版本（强制该用户重新登录）。
// defaultRole 仅在平台用户表中无此用户（首次改密的 config 用户）时使用；已有用户保留原角色。
func UserSetPassword(ctx context.Context, db *sql.DB, dialect, name, pwdHash, defaultRole string) error {
	var ver int
	role := ""
	_ = db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT token_version, role FROM platform_user WHERE uname=?`), name).Scan(&ver, &role)
	if strings.TrimSpace(role) == "" {
		role = defaultRole
	}
	if strings.TrimSpace(role) == "" {
		role = "editor"
	}
	if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM platform_user WHERE uname=?`), name); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO platform_user (uname, pwd_hash, token_version, role) VALUES (?,?,?,?)`),
		name, pwdHash, ver+1, role)
	return err
}

// UserAuthInfo 查询平台用户 (hash, tokenVersion, role)。
func UserAuthInfo(ctx context.Context, db *sql.DB, dialect, name string) (string, int, string, error) {
	var h, role string
	var v int
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT pwd_hash, token_version, role FROM platform_user WHERE uname=?`), name).
		Scan(&h, &v, &role)
	if err == sql.ErrNoRows {
		return "", 0, "", nil
	}
	if err != nil {
		return "", 0, "", err
	}
	return h, v, role, nil
}

// UserRow 平台/配置用户视图。
type UserRow struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Source string `json:"source"` // config / db
}

// UserListDB 列出平台用户表中的用户。
func UserListDB(ctx context.Context, db *sql.DB, dialect string) ([]UserRow, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT uname, role FROM platform_user ORDER BY uname`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.Name, &u.Role); err != nil {
			return nil, err
		}
		u.Source = "db"
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserRole 更新用户角色。
func UserRole(ctx context.Context, db *sql.DB, dialect, name, role string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE platform_user SET role=?, token_version=token_version+1 WHERE uname=?`), role, name)
	return err
}

// UserDeleteDB 删除平台用户（其会话随记录消失）。
func UserDeleteDB(ctx context.Context, db *sql.DB, dialect, name string) error {
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM platform_user WHERE uname=?`), name)
	return err
}

// ───── SQL 执行历史 ─────

type SQLHistoryRow struct {
	ID         string `json:"id"`
	UserName   string `json:"userName"`
	DbSource   string `json:"dbSource"`
	SQLText    string `json:"sqlText"`
	DurationMS int64  `json:"durationMs"`
	RowCount   int    `json:"rowCount"`
	OK         string `json:"ok"`
	ErrMsg     string `json:"errMsg"`
	CreateTime string `json:"createTime"`
}

func SQLHistoryInsert(ctx context.Context, db *sql.DB, dialect string, h SQLHistoryRow) error {
	if h.ID == "" {
		h.ID = NewID()
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO onl_sql_history (id, user_name, db_source, sql_text, duration_ms, row_count, ok, err_msg, create_time)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		h.ID, h.UserName, h.DbSource, h.SQLText, h.DurationMS, h.RowCount, h.OK, h.ErrMsg, time.Now())
	return err
}

func SQLHistoryList(ctx context.Context, db *sql.DB, dialect, user string, page, size int) ([]SQLHistoryRow, int, error) {
	where := " WHERE 1=1"
	var args []any
	if user != "" {
		where += " AND user_name=?"
		args = append(args, user)
	}
	var total int
	if err := db.QueryRowContext(ctx, dbx.Rebind(dialect, "SELECT COUNT(*) FROM onl_sql_history"+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	q := dbx.PageWrap(dialect, `SELECT id, user_name, db_source, sql_text, duration_ms, row_count, ok, err_msg, create_time FROM onl_sql_history`+where+` ORDER BY create_time DESC`, page, size)
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []SQLHistoryRow{}
	for rows.Next() {
		var h SQLHistoryRow
		var ct sql.NullTime
		if err := rows.Scan(&h.ID, &h.UserName, &h.DbSource, &h.SQLText, &h.DurationMS, &h.RowCount, &h.OK, &h.ErrMsg, &ct); err != nil {
			return nil, 0, err
		}
		h.CreateTime = FmtNullTime(ct)
		out = append(out, h)
	}
	return out, total, rows.Err()
}

// UserSetPasswordTx 事务版：删除+插入+角色三步绑定在事务中。
// role 为空时保留用户原有角色；token_version 取自删除前的行（+1 强制下线）。
func UserSetPasswordTx(ctx context.Context, db *sql.DB, dialect, name, pwdHash, role string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ver int
	var oldRole string
	_ = tx.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT token_version, role FROM platform_user WHERE uname=?`), name).Scan(&ver, &oldRole)
	if strings.TrimSpace(role) == "" {
		role = oldRole
	}
	if strings.TrimSpace(role) == "" {
		role = "editor"
	}
	if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM platform_user WHERE uname=?`), name); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO platform_user (uname, pwd_hash, token_version, role) VALUES (?,?,?,?)`),
		name, pwdHash, ver+1, role); err != nil {
		return err
	}
	return tx.Commit()
}
