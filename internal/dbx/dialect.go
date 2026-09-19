// Package dbx 提供多数据库方言抽象：
// 统一使用 "?" 占位符书写 SQL，由 Rebind 按方言转换；
// 分页、取元数据、类型映射、标识符引用等按方言分发。
// 支持 sqlite / mysql / postgresql(含人大金仓) / oracle / sqlserver / clickhouse（均为纯 Go 驱动）。
package dbx

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	SQLite     = "sqlite"
	MySQL      = "mysql"
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
	SQLServer  = "sqlserver"
	ClickHouse = "clickhouse"
)

// Normalize 归一化数据库类型名。
func Normalize(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "sqlite", "sqlite3":
		return SQLite
	case "mysql", "mariadb":
		return MySQL
	case "postgresql", "postgres", "pgsql", "pg", "kingbase", "kingbasees":
		// 人大金仓兼容 PostgreSQL 协议
		return PostgreSQL
	case "oracle", "orcl":
		return Oracle
	case "sqlserver", "mssql", "ms":
		return SQLServer
	case "clickhouse", "ch":
		return ClickHouse
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

// DriverName 返回 database/sql 驱动注册名。
func DriverName(dialect string) string {
	switch dialect {
	case MySQL:
		return "mysql"
	case PostgreSQL:
		return "postgres"
	case Oracle:
		return "oracle"
	case SQLServer:
		return "sqlserver"
	case ClickHouse:
		return "clickhouse"
	default:
		return "sqlite"
	}
}

// Rebind 把 "?" 占位符转换为方言形式（$1.. / :1.. / @p1..）。
// 跳过单引号字符串字面量内的 "?"，避免占位符编号错位。
func Rebind(dialect, query string) string {
	var prefix byte
	switch dialect {
	case PostgreSQL:
		prefix = '$'
	case Oracle:
		prefix = ':'
	case SQLServer:
		prefix = '@'
	default:
		return query // mysql / sqlite / clickhouse 原生 "?"
	}
	var b strings.Builder
	n := 0
	inLit := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '\'' {
			inLit = !inLit
			b.WriteByte(c)
			continue
		}
		if c == '?' && !inLit {
			n++
			b.WriteByte(prefix)
			if prefix == '@' {
				b.WriteByte('p')
			}
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Quote 引用标识符（表名/列名）。调用方必须先用 ValidIdent 校验；
// 此处仍转义内嵌引号字符作为纵深防御。
func Quote(dialect, ident string) string {
	switch dialect {
	case MySQL, SQLite, ClickHouse:
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	case SQLServer:
		return "[" + strings.ReplaceAll(ident, "]", "]]") + "]"
	case Oracle:
		// Oracle 未加引号标识符自动转大写，便于匹配常规建表；
		// 调用方已用 ValidIdent 白名单校验，不含引号/空格等特殊字符
		return ident
	default: // postgresql：加引号可避免保留字（order/group 等）冲突
		return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
	}
}

var identBad = func(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '$')
}

// ValidIdent 校验标识符，防止注入。
func ValidIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	return !strings.ContainsFunc(s, identBad)
}

// PageWrap 分页包装，page 从 1 开始。
func PageWrap(dialect, inner string, page, size int) string {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	offset := (page - 1) * size
	hi := offset + size
	switch dialect {
	case Oracle:
		return fmt.Sprintf(
			"SELECT * FROM ( SELECT litrpt_t.*, ROWNUM litrpt_rn FROM ( %s ) litrpt_t WHERE ROWNUM <= %d ) WHERE litrpt_rn > %d",
			inner, hi, offset)
	case SQLServer:
		// OFFSET/FETCH 要求 ORDER BY
		return fmt.Sprintf("SELECT * FROM ( %s ) litrpt_page ORDER BY (SELECT NULL) OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", inner, offset, size)
	default: // mysql / sqlite / postgresql / clickhouse
		return fmt.Sprintf("SELECT * FROM ( %s ) litrpt_page LIMIT %d OFFSET %d", inner, size, offset)
	}
}

// LimitWrap 取一条记录用于解析字段元数据。
func LimitWrap(dialect, inner string) string {
	return LimitWrapN(dialect, inner, 1)
}

// LimitWrapN 取前 n 条记录，按方言生成（Oracle 无 LIMIT、SQLServer 用 TOP）。
func LimitWrapN(dialect, inner string, n int) string {
	if n < 1 {
		n = 1
	}
	switch dialect {
	case Oracle:
		return fmt.Sprintf("SELECT * FROM ( %s ) litrpt_top WHERE ROWNUM <= %d", inner, n)
	case SQLServer:
		return fmt.Sprintf("SELECT TOP %d * FROM ( %s ) litrpt_top", n, inner)
	default:
		return fmt.Sprintf("SELECT * FROM ( %s ) litrpt_top LIMIT %d", inner, n)
	}
}

// MapType 把数据库列类型映射为报表字段类型（与页面一致：字符类型/数值类型/日期类型）。
func MapType(dialect, dbTypeName string) string {
	t := strings.ToUpper(dbTypeName)
	switch dialect {
	case Oracle:
		switch {
		case strings.Contains(t, "NUMBER") || strings.Contains(t, "INT") ||
			strings.Contains(t, "FLOAT") || strings.Contains(t, "DECIMAL") || strings.Contains(t, "REAL"):
			return "数值类型"
		case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
			return "日期类型"
		default:
			return "字符类型"
		}
	case ClickHouse:
		switch {
		case strings.Contains(t, "INT") || strings.Contains(t, "FLOAT") || strings.Contains(t, "DECIMAL") ||
			strings.Contains(t, "UINT") || strings.Contains(t, "NUMERIC"):
			return "数值类型"
		case strings.Contains(t, "DATE"):
			return "日期类型"
		default:
			return "字符类型"
		}
	}
	switch {
	case strings.Contains(t, "INT") || strings.Contains(t, "DEC") || strings.Contains(t, "NUM") ||
		strings.Contains(t, "REAL") || strings.Contains(t, "FLOAT") || strings.Contains(t, "DOUB") || strings.Contains(t, "MONEY"):
		return "数值类型"
	case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
		return "日期类型"
	default:
		return "字符类型"
	}
}

// TranslateDDL 把通用建表 DDL 翻译为方言 DDL（用于元数据表与示例表）。
// 书写约定：VARCHAR(n)/TEXT/NUMERIC(p,s)/INT/TIMESTAMP。
func TranslateDDL(ddl, dialect string) string {
	switch dialect {
	case Oracle:
		r := strings.NewReplacer(
			"TEXT", "CLOB",
			"VARCHAR(", "VARCHAR2(",
			"NUMERIC(", "NUMBER(",
		)
		return r.Replace(ddl)
	case SQLServer:
		r := strings.NewReplacer(
			"TEXT", "NVARCHAR(MAX)",
			"TIMESTAMP", "DATETIME2",
			"NUMERIC(", "DECIMAL(",
		)
		return r.Replace(ddl)
	case ClickHouse:
		r := strings.NewReplacer(
			"VARCHAR(36)", "FixedString(36)",
			"VARCHAR(", "String",
			"TEXT", "String",
			"NUMERIC(", "Decimal(18,4)",
			"TIMESTAMP", "DateTime",
			"INT", "Int32",
		)
		return r.Replace(ddl)
	default:
		return ddl
	}
}

// SupportsInlineIndex 方言是否支持 CREATE [UNIQUE] INDEX IF NOT EXISTS 语法。
// ClickHouse 无普通二级索引，SQLServer 语法不同，建索引语句由调用方跳过或特殊处理。
func SupportsInlineIndex(dialect string) bool {
	switch dialect {
	case ClickHouse:
		return false
	default:
		return true
	}
}

// DDLStatement 方言化单条 DDL：处理 IF NOT EXISTS 与 SQLServer/Oracle 的存在性守卫。
func DDLStatement(genericDDL, dialect string) (string, bool) {
	translated := TranslateDDL(genericDDL, dialect)
	isIndex := strings.Contains(strings.ToUpper(genericDDL), "CREATE INDEX") ||
		strings.Contains(strings.ToUpper(genericDDL), "CREATE UNIQUE INDEX")
	switch dialect {
	case Oracle:
		return oracleGuard(translated), true
	case SQLServer:
		// 依据语句内首个对象名做存在性守卫
		name := firstObjectName(genericDDL)
		if isIndex {
			return fmt.Sprintf(
				"IF NOT EXISTS (SELECT * FROM sys.indexes WHERE name = '%s') EXEC('%s')", name, strings.ReplaceAll(translated, "'", "''")), true
		}
		return fmt.Sprintf(
			"IF OBJECT_ID(N'%s', N'U') IS NULL AND OBJECT_ID(N'%s', N'V') IS NULL EXEC('%s')", name, name, strings.ReplaceAll(translated, "'", "''")), true
	case ClickHouse:
		if isIndex {
			return "", false // 跳过
		}
		return translated, true
	default:
		out := translated
		if isIndex {
			out = strings.Replace(translated, "CREATE UNIQUE INDEX", "CREATE UNIQUE INDEX IF NOT EXISTS", 1)
			out = strings.Replace(out, "CREATE INDEX", "CREATE INDEX IF NOT EXISTS", 1)
		} else {
			out = strings.Replace(translated, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)
		}
		return out, true
	}
}

// firstObjectName 提取 CREATE [UNIQUE] INDEX/TABLE 后的对象名。
func firstObjectName(ddl string) string {
	up := strings.ToUpper(ddl)
	idx := strings.Index(up, "CREATE")
	if idx < 0 {
		return "litereport_obj"
	}
	rest := ddl[idx:]
	for _, kw := range []string{"CREATE UNIQUE INDEX ", "CREATE INDEX ", "CREATE TABLE "} {
		if len(rest) >= len(kw) && strings.EqualFold(rest[:len(kw)], kw) {
			name := rest[len(kw):]
			if i := strings.IndexAny(name, " (\r\n"); i >= 0 {
				name = name[:i]
			}
			return strings.TrimSpace(name)
		}
	}
	return "litereport_obj"
}

func oracleGuard(stmt string) string {
	return fmt.Sprintf(`BEGIN EXECUTE IMMEDIATE '%s'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;`,
		strings.ReplaceAll(stmt, "'", "''"))
}
