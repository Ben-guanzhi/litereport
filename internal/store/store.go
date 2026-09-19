// Package store 报表配置元数据的存储层（头表/字段明细/参数表）。
// 全部 SQL 以 "?" 占位符书写，经 dbx.Rebind 适配 sqlite/mysql/postgresql/oracle。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"time"

	"litereport/internal/dbx"
	"litereport/internal/model"
)

var ErrNotFound = errors.New("记录不存在")

// EnsureTables 幂等创建元数据表（含 Oracle 的 IF NOT EXISTS 兼容处理）。
func EnsureTables(db *sql.DB, dialect string) error {
	stmts := []string{
		`CREATE TABLE onl_cgreport_head (
			id VARCHAR(36) PRIMARY KEY,
			report_name VARCHAR(200) NOT NULL,
			report_code VARCHAR(100) NOT NULL,
			cg_sql TEXT,
			db_source VARCHAR(100),
			perm_filter TEXT,
			perm_enabled VARCHAR(2),
			create_by VARCHAR(64),
			create_time TIMESTAMP,
			update_by VARCHAR(64),
			update_time TIMESTAMP
		)`,
		`CREATE TABLE onl_cgreport_item (
			id VARCHAR(36) PRIMARY KEY,
			head_id VARCHAR(36) NOT NULL,
			field_name VARCHAR(100),
			field_txt VARCHAR(300),
			field_type VARCHAR(20),
			field_href VARCHAR(300),
			is_show VARCHAR(2) DEFAULT 'Y',
			is_query VARCHAR(2) DEFAULT 'N',
			query_mode VARCHAR(20) DEFAULT '=',
			dict_code VARCHAR(200),
			group_title VARCHAR(100),
			order_num INT,
			width INT,
			is_total VARCHAR(2) DEFAULT 'N',
			render_rule VARCHAR(500)
		)`,
		`CREATE TABLE onl_cgreport_param (
			id VARCHAR(36) PRIMARY KEY,
			head_id VARCHAR(36) NOT NULL,
			param_name VARCHAR(100),
			param_txt VARCHAR(200),
			param_value VARCHAR(500),
			order_num INT
		)`,
		`CREATE UNIQUE INDEX uk_onl_cgreport_code ON onl_cgreport_head(report_code)`,
		// 子表按 head_id 关联查询走索引，避免全表扫描
		`CREATE INDEX idx_cgreport_item_head ON onl_cgreport_item(head_id)`,
		`CREATE INDEX idx_cgreport_param_head ON onl_cgreport_param(head_id)`,
	}
	for _, s := range stmts {
		q, ok := dbx.DDLStatement(s, dialect)
		if !ok {
			continue
		}
		if _, err := db.Exec(dbx.Rebind(dialect, q)); err != nil {
			return fmt.Errorf("初始化元数据表失败: %w", err)
		}
	}
	return nil
}

func scanHead(row interface{ Scan(...any) error }) (*model.Head, error) {
	var h model.Head
	var ct, ut sql.NullTime
	var pf, pe, cat, stok sql.NullString // 存量行 perm/category/share 列可能为 NULL
	var sexp sql.NullTime
	if err := row.Scan(&h.ID, &h.ReportName, &h.ReportCode, &h.CgSQL, &h.DbSource,
		&pf, &pe, &cat, &stok, &sexp, &h.CreateBy, &ct, &h.UpdateBy, &ut); err != nil {
		return nil, err
	}
	h.PermFilter, h.PermEnabled = pf.String, pe.String
	h.Category = cat.String
	h.ShareToken = stok.String
	if sexp.Valid {
		h.ShareExpire = sexp.Time.Format("2006-01-02 15:04:05")
	}
	h.CreateTime = fmtTime(ct)
	h.UpdateTime = fmtTime(ut)
	return &h, nil
}

// HeadSetShare 设置/撤销(空token)报表分享令牌。
func HeadSetShare(ctx context.Context, db *sql.DB, dialect, id, token string, expire *time.Time) error {
	var exp any
	if expire != nil {
		exp = *expire
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`UPDATE onl_cgreport_head SET share_token=?, share_expire=? WHERE id=?`), token, exp, id)
	return err
}

// HeadCodesBySource 查询引用指定数据源的报表编码（删除数据源前的引用检查）。
func HeadCodesBySource(ctx context.Context, db *sql.DB, dialect, source string) ([]string, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		"SELECT report_code FROM onl_cgreport_head WHERE db_source=?"), source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func fmtTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02 15:04:05")
}

const headCols = "id, report_name, report_code, cg_sql, db_source, perm_filter, perm_enabled, category, share_token, share_expire, create_by, create_time, update_by, update_time"

// HeadList 分页查询报表配置。
func HeadList(ctx context.Context, db *sql.DB, dialect, reportName, reportCode, category string, page, size int) ([]model.Head, int, error) {
	where := " WHERE 1=1"
	var args []any
	if reportName != "" {
		where += " AND report_name LIKE ?"
		args = append(args, "%"+reportName+"%")
	}
	if reportCode != "" {
		where += " AND report_code LIKE ?"
		args = append(args, "%"+reportCode+"%")
	}
	if category != "" {
		where += " AND category = ?"
		args = append(args, category)
	}
	var total int
	if err := db.QueryRowContext(ctx, dbx.Rebind(dialect, "SELECT COUNT(*) FROM onl_cgreport_head"+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	q := dbx.PageWrap(dialect, "SELECT "+headCols+" FROM onl_cgreport_head"+where+" ORDER BY create_time DESC", page, size)
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect, q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []model.Head{}
	for rows.Next() {
		h, err := scanHead(rows)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, *h)
	}
	return list, total, nil
}

// HeadGet 按 id 查询。
func HeadGet(ctx context.Context, db *sql.DB, dialect, id string) (*model.Head, error) {
	h, err := scanHead(db.QueryRowContext(ctx, dbx.Rebind(dialect,
		"SELECT "+headCols+" FROM onl_cgreport_head WHERE id = ?"), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// HeadGetByCode 按报表编码查询。
func HeadGetByCode(ctx context.Context, db *sql.DB, dialect, code string) (*model.Head, error) {
	h, err := scanHead(db.QueryRowContext(ctx, dbx.Rebind(dialect,
		"SELECT "+headCols+" FROM onl_cgreport_head WHERE report_code = ?"), code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// HeadFindByKey 优先按 id、再按 code 查询。
func HeadFindByKey(ctx context.Context, db *sql.DB, dialect, key string) (*model.Head, error) {
	h, err := HeadGet(ctx, db, dialect, key)
	if err == nil {
		return h, nil
	}
	return HeadGetByCode(ctx, db, dialect, key)
}

// HeadCount 统计报表数量（用于首次启动种子数据判断）。
func HeadCount(ctx context.Context, db *sql.DB, dialect string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect, "SELECT COUNT(*) FROM onl_cgreport_head")).Scan(&n)
	return n, err
}

// HeadSave 保存报表配置（头 + 明细 + 参数），存在则更新，整事务替换子表。
func HeadSave(ctx context.Context, db *sql.DB, dialect string, h *model.Head, items []model.Item, params []model.Param) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists := false
	var n int
	_ = tx.QueryRowContext(ctx, dbx.Rebind(dialect, "SELECT COUNT(*) FROM onl_cgreport_head WHERE id = ?"), h.ID).Scan(&n)
	exists = n > 0
	if exists {
		_, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
			`UPDATE onl_cgreport_head SET report_name=?, report_code=?, cg_sql=?, db_source=?, perm_filter=?, perm_enabled=?, category=?, update_by=?, update_time=? WHERE id=?`),
			h.ReportName, h.ReportCode, h.CgSQL, h.DbSource, h.PermFilter, h.PermEnabled, h.Category, h.UpdateBy, nowArg(), h.ID)
	} else {
		_, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_cgreport_head (id, report_name, report_code, cg_sql, db_source, perm_filter, perm_enabled, category, create_by, create_time, update_by, update_time)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
			h.ID, h.ReportName, h.ReportCode, h.CgSQL, h.DbSource, h.PermFilter, h.PermEnabled, h.Category, h.CreateBy, nowArg(), h.UpdateBy, nowArg())
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect, "DELETE FROM onl_cgreport_item WHERE head_id=?"), h.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect, "DELETE FROM onl_cgreport_param WHERE head_id=?"), h.ID); err != nil {
		return err
	}
	for i := range items {
		it := items[i]
		if it.ID == "" {
			it.ID = newID()
		}
		if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_cgreport_item (id, head_id, field_name, field_txt, field_type, field_href,
			 is_show, is_query, query_mode, dict_code, group_title, order_num, width, is_total, render_rule)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
			it.ID, h.ID, it.FieldName, it.FieldTxt, it.FieldType, it.FieldHref,
			it.IsShow, it.IsQuery, it.QueryMode, it.DictCode, it.GroupTitle, it.OrderNum, it.Width, it.IsTotal, it.RenderRule); err != nil {
			return err
		}
	}
	for i := range params {
		p := params[i]
		if p.ID == "" {
			p.ID = newID()
		}
		if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect,
			`INSERT INTO onl_cgreport_param (id, head_id, param_name, param_txt, param_value, order_num)
			 VALUES (?,?,?,?,?,?)`),
			p.ID, h.ID, p.ParamName, p.ParamTxt, p.ParamValue, p.OrderNum); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HeadDelete 删除报表配置及子表。
func HeadDelete(ctx context.Context, db *sql.DB, dialect, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"DELETE FROM onl_cgreport_item WHERE head_id=?",
		"DELETE FROM onl_cgreport_param WHERE head_id=?",
		"DELETE FROM onl_cgreport_head WHERE id=?",
	} {
		if _, err = tx.ExecContext(ctx, dbx.Rebind(dialect, q), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ItemsByHead(ctx context.Context, db *sql.DB, dialect, headID string) ([]model.Item, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, head_id, field_name, field_txt, field_type, field_href, is_show, is_query,
		        query_mode, dict_code, group_title, order_num, width, is_total, render_rule
		 FROM onl_cgreport_item WHERE head_id=? ORDER BY order_num ASC, id ASC`), headID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []model.Item{}
	for rows.Next() {
		var it model.Item
		var rr sql.NullString // 存量行 render_rule 可能为 NULL
		if err := rows.Scan(&it.ID, &it.HeadID, &it.FieldName, &it.FieldTxt, &it.FieldType, &it.FieldHref,
			&it.IsShow, &it.IsQuery, &it.QueryMode, &it.DictCode, &it.GroupTitle, &it.OrderNum, &it.Width, &it.IsTotal, &rr); err != nil {
			return nil, err
		}
		it.RenderRule = rr.String
		list = append(list, it)
	}
	return list, rows.Err()
}

func ParamsByHead(ctx context.Context, db *sql.DB, dialect, headID string) ([]model.Param, error) {
	rows, err := db.QueryContext(ctx, dbx.Rebind(dialect,
		`SELECT id, head_id, param_name, param_txt, param_value, order_num
		 FROM onl_cgreport_param WHERE head_id=? ORDER BY order_num ASC, id ASC`), headID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []model.Param{}
	for rows.Next() {
		var p model.Param
		if err := rows.Scan(&p.ID, &p.HeadID, &p.ParamName, &p.ParamTxt, &p.ParamValue, &p.OrderNum); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}
