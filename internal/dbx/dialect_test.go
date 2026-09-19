package dbx

import (
	"strings"
	"testing"
)

func TestSQLServerDialect(t *testing.T) {
	if got := Rebind(SQLServer, "a=? and b=?"); got != "a=@p1 and b=@p2" {
		t.Errorf("mssql rebind: %s", got)
	}
	if got := Quote(SQLServer, "order"); got != "[order]" {
		t.Errorf("mssql quote: %s", got)
	}
	pw := PageWrap(SQLServer, "select * from t", 2, 10)
	if !strings.Contains(pw, "OFFSET 10 ROWS FETCH NEXT 10 ROWS ONLY") {
		t.Errorf("mssql page: %s", pw)
	}
	if !strings.Contains(LimitWrap(SQLServer, "select * from t"), "TOP 1") {
		t.Error("mssql limit 应使用 TOP 1")
	}
	mt := MapType(SQLServer, "nvarchar")
	if mt != "字符类型" {
		t.Errorf("mssql nvarchar: %s", mt)
	}
	if MapType(SQLServer, "datetime") != "日期类型" {
		t.Error("mssql datetime 应为日期")
	}
	ddl, ok := DDLStatement("CREATE TABLE demo (id VARCHAR(36) PRIMARY KEY, remark TEXT)", SQLServer)
	if !ok || !strings.Contains(ddl, "IF OBJECT_ID") || !strings.Contains(ddl, "NVARCHAR(MAX)") {
		t.Errorf("mssql ddl: %s", ddl)
	}
}

func TestClickHouseDialect(t *testing.T) {
	if Rebind(ClickHouse, "a=?") != "a=?" {
		t.Error("clickhouse 原生 ? 占位符")
	}
	if got := PageWrap(ClickHouse, "select * from t", 1, 10); !strings.Contains(got, "LIMIT 10 OFFSET 0") {
		t.Errorf("ch page: %s", got)
	}
	ddl, ok := DDLStatement("CREATE TABLE demo (id VARCHAR(36) PRIMARY KEY, remark TEXT)", ClickHouse)
	if !ok || !strings.Contains(ddl, "FixedString(36)") || !strings.Contains(ddl, "String") {
		t.Errorf("ch ddl: %s", ddl)
	}
	// clickhouse 跳过二级索引
	_, ok2 := DDLStatement("CREATE INDEX idx_a ON demo(a)", ClickHouse)
	if ok2 {
		t.Error("clickhouse 应跳过普通索引")
	}
}

func TestKingbaseAlias(t *testing.T) {
	if Normalize("kingbase") != PostgreSQL {
		t.Error("人大金仓应映射到 postgresql")
	}
}

// TestLimitWrapNDialects 回归：LIMIT 包裹必须按方言生成（Oracle 无 LIMIT、SQLServer 用 TOP）。
func TestLimitWrapNDialects(t *testing.T) {
	inner := "select name, count(*) as c from t group by name order by c desc"
	cases := []struct {
		dialect  string
		contains []string
		excludes []string
	}{
		{Oracle, []string{"ROWNUM <= 5"}, []string{"LIMIT"}},
		{SQLServer, []string{"TOP 5"}, []string{"LIMIT"}},
		{ClickHouse, []string{"LIMIT 5"}, nil},
		{MySQL, []string{"LIMIT 5"}, nil},
		{PostgreSQL, []string{"LIMIT 5"}, nil},
		{SQLite, []string{"LIMIT 5"}, nil},
	}
	for _, c := range cases {
		got := LimitWrapN(c.dialect, inner, 5)
		for _, s := range c.contains {
			if !strings.Contains(got, s) {
				t.Errorf("%s LimitWrapN 应包含 %q: %s", c.dialect, s, got)
			}
		}
		for _, s := range c.excludes {
			if strings.Contains(strings.ToUpper(got), s) {
				t.Errorf("%s LimitWrapN 不应包含 %q: %s", c.dialect, s, got)
			}
		}
	}
	if LimitWrap(Oracle, inner) != LimitWrapN(Oracle, inner, 1) {
		t.Error("LimitWrap 应等价于 LimitWrapN(n=1)")
	}
}

// TestPageWrapDialects 分页包装的方言矩阵（图表/列表共用链路）。
func TestPageWrapDialects(t *testing.T) {
	cases := []struct {
		dialect  string
		contains []string
	}{
		{Oracle, []string{"ROWNUM <= 40", "litrpt_rn > 20"}},
		{SQLServer, []string{"OFFSET 20 ROWS FETCH NEXT 20 ROWS ONLY"}},
		{PostgreSQL, []string{"LIMIT 20 OFFSET 20"}},
		{ClickHouse, []string{"LIMIT 20 OFFSET 20"}},
	}
	for _, c := range cases {
		got := PageWrap(c.dialect, "select * from t", 2, 20)
		for _, s := range c.contains {
			if !strings.Contains(got, s) {
				t.Errorf("%s PageWrap 应包含 %q: %s", c.dialect, s, got)
			}
		}
	}
}
