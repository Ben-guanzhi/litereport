// 元库备份：全量配置导出（JSON）与 SQLite 滚动备份。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"litereport/internal/dbx"
)

// backupTables 参与全量导出的元数据表（不含大体积的历史/快照表）。
var backupTables = []string{
	"onl_cgreport_head", "onl_cgreport_item", "onl_cgreport_param",
	"sys_dict", "onl_chart", "onl_report_push", "onl_form", "onl_datasource",
	"platform_user", "platform_schema",
}

// BackupExport 导出全部元数据表为通用行集（表名均为代码内常量）。
// DSN 为密文原样导出，密钥不导出——恢复需配置相同 auth.secret。
func BackupExport(ctx context.Context, db *sql.DB, dialect string) (map[string]any, error) {
	tables := map[string][]map[string]any{}
	for _, t := range backupTables {
		list, err := dumpTable(ctx, db, t)
		if err != nil {
			return nil, err
		}
		tables[t] = list
	}
	return map[string]any{
		"exported_at": time.Now().Format("2006-01-02 15:04:05"),
		"product":     "litereport",
		"tables":      tables,
	}, nil
}

func dumpTable(ctx context.Context, db *sql.DB, table string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("读取%s失败: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	list := []map[string]any{}
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
			v := vals[i]
			switch t := v.(type) {
			case time.Time:
				v = t.Format("2006-01-02 15:04:05")
			case []byte:
				v = string(t)
			}
			rec[c] = v
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

// SQLiteBackupPath 对 sqlite 元库执行 VACUUM INTO 到指定文件（文件须不存在）。
func SQLiteBackupPath(ctx context.Context, db *sql.DB, path string) error {
	_, err := db.ExecContext(ctx, "VACUUM INTO '"+path+"'")
	return err
}

// restoreTables 恢复时允许覆盖的表（不含 platform_user 防止改掉当前管理员密码、
// 不含 platform_schema 防止回退迁移版本、不含历史/快照大表）。
var restoreTables = []string{
	"onl_cgreport_head", "onl_cgreport_item", "onl_cgreport_param",
	"sys_dict", "onl_chart", "onl_report_push", "onl_form", "onl_datasource",
}

// BackupRestore 用导出的 JSON 数据整表替换恢复配置（单事务，任一步失败整体回滚）。
// tables 键必须落在 restoreTables 白名单内，列名以数据库实际列校验，防注入。
func BackupRestore(ctx context.Context, db *sql.DB, dialect string, tables map[string][]map[string]any) (int, error) {
	validCols := map[string]map[string]bool{}
	for _, t := range restoreTables {
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+t+" WHERE 1=0")
		if err != nil {
			return 0, fmt.Errorf("读取%s列失败: %w", t, err)
		}
		cols, _ := rows.Columns()
		rows.Close()
		set := map[string]bool{}
		for _, c := range cols {
			set[strings.ToLower(c)] = true
		}
		validCols[t] = set
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	total := 0
	for _, t := range restoreTables {
		rows, ok := tables[t]
		if !ok {
			continue // 备份文件缺该表则跳过（保留现状）
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return 0, fmt.Errorf("清空%s失败: %w", t, err)
		}
		for _, rec := range rows {
			var cols []string
			var args []any
			for k, v := range rec {
				ck := strings.ToLower(k)
				if !validCols[t][ck] {
					continue
				}
				cols = append(cols, ck)
				args = append(args, v)
			}
			if len(cols) == 0 {
				continue
			}
			idx := make([]int, len(cols))
			for i := range idx {
				idx[i] = i
			}
			sort.Slice(idx, func(a, b int) bool { return cols[idx[a]] < cols[idx[b]] })
			sortedCols := make([]string, len(cols))
			sortedArgs := make([]any, len(args))
			for i, j := range idx {
				sortedCols[i] = cols[j]
				sortedArgs[i] = args[j]
			}
			ph := strings.TrimSuffix(strings.Repeat("?,", len(sortedCols)), ",")
			quoted := make([]string, len(sortedCols))
			for i, c := range sortedCols {
				quoted[i] = dbx.Quote(dialect, c)
			}
			q := "INSERT INTO " + t + " (" + strings.Join(quoted, ", ") + ") VALUES (" + ph + ")"
			if _, err := tx.ExecContext(ctx, dbx.Rebind(dialect, q), sortedArgs...); err != nil {
				return 0, fmt.Errorf("写入%s失败: %w", t, err)
			}
			total++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}
