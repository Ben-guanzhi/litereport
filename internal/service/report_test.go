package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"litereport/internal/dbx"
	"litereport/internal/model"
)

// testDB 创建带 demo 表的临时 sqlite 库。
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbx.Open("sqlite", t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE demo (
		id VARCHAR(36) PRIMARY KEY, name VARCHAR(100), sex VARCHAR(10), age INT, salary NUMERIC(12,2))`); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		{"demo0001", "小王1", "男", 28, 1221.22},
		{"demo0002", "scott", "男", 18, 1133.23},
		{"demo0004", "王五5", "男", nil, 99},
		{"demo0005", "小张", "女", 21, nil},
	}
	for _, r := range rows {
		if _, err = db.Exec(`INSERT INTO demo (id, name, sex, age, salary) VALUES (?,?,?,?,?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func testItems() []model.Item {
	return []model.Item{
		{FieldName: "id", FieldTxt: "id", IsShow: "Y", IsQuery: "N", QueryMode: "="},
		{FieldName: "name", FieldTxt: "name", IsShow: "Y", IsQuery: "Y", QueryMode: "like"},
		{FieldName: "sex", FieldTxt: "sex", IsShow: "Y", IsQuery: "Y", QueryMode: "="},
		{FieldName: "age", FieldTxt: "age", IsShow: "Y", IsQuery: "Y", QueryMode: ">="},
		{FieldName: "salary", FieldTxt: "salary", FieldType: "数值类型", IsShow: "Y", IsQuery: "N", QueryMode: "=", IsTotal: "Y"},
	}
}

func TestCleanSQLGuards(t *testing.T) {
	bad := []string{
		"select 1; drop table demo",
		"select 1 ;drop table demo",
		"select 1;select 2",
		"update demo set name='x'",
		"DELETE FROM demo",
		"select * from demo where name = ?",
		"",
	}
	for _, s := range bad {
		if _, err := CleanSQL(s); err == nil {
			t.Errorf("应当拒绝: %q", s)
		}
	}
	if _, err := CleanSQL("  select * from demo ; "); err != nil {
		t.Errorf("合法查询被误拒: %v", err)
	}
}

// 过滤值必须走绑定参数，注入串只能当普通字符串匹配。
func TestQueryFilterInjection(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	cases := map[string]string{
		"like等值":  `' OR '1'='1`,
		"union注入": `%' UNION SELECT id,name,sex,age,salary FROM demo--`,
		"or永真":    `x' OR '1'='1' --`,
		"括号闭合":    `a') OR ('1'='1`,
	}
	for label, payload := range cases {
		q := map[string][]string{"name": {payload}}
		res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q)
		if err != nil {
			t.Errorf("[%s] 查询报错: %v", label, err)
			continue
		}
		if res.Total != 0 || len(res.Records) != 0 {
			t.Errorf("[%s] 注入串 %q 返回了 %d 条记录，期望 0", label, payload, res.Total)
		}
	}
	// = 模式注入
	q := map[string][]string{"sex": {`男' OR '1'='1`}}
	if res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil || res.Total != 0 {
		t.Errorf("=模式注入未被拦截: total=%d err=%v", res.Total, err)
	}
	// 正常值不受影响（like 王 匹配 小王1、王五5）
	q = map[string][]string{"name": {"王"}}
	if res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil || res.Total != 2 {
		t.Errorf("正常like查询失败: total=%d err=%v", res.Total, err)
	}
}

// 排序列必须匹配已配置字段且为合法标识符。
func TestSortInjection(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	for _, col := range []string{"age; DROP TABLE demo", "(SELECT 1)", "1=1", "age--"} {
		q := map[string][]string{"column": {col}, "order": {"desc"}}
		if res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil || res.Total != 4 {
			t.Errorf("排序注入 %q 未被忽略: total=%d err=%v", col, res.Total, err)
		}
	}
	// order 方向只允许 asc/desc
	q := map[string][]string{"column": {"age"}, "order": {"desc; DROP TABLE demo"}}
	if _, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil {
		t.Errorf("非法排序方向导致报错: %v", err)
	}
	// 合法排序仍可用
	res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{"column": {"age"}, "order": {"desc"}})
	if err != nil || len(res.Records) == 0 || res.Records[0]["name"] != "小王1" {
		t.Errorf("合法排序失败: err=%v first=%v", err, res)
	}
}

// ${x} 在字面量内（like '%${kw}%'）与整体作为值（'${sex}'）都必须正确绑定。
func TestLiteralParamRewrite(t *testing.T) {
	db := testDB(t)
	// like 字面量内嵌参数
	head := &model.Head{CgSQL: "select * from demo where name like '%${kw}%'"}
	q := map[string][]string{"kw": {"王"}}
	res, err := QueryReport(context.Background(), db, "sqlite", head, nil, nil, q)
	if err != nil || res.Total != 2 {
		t.Fatalf("like内嵌参数失败: total=%d err=%v", res.Total, err)
	}
	// 整体作为值
	head2 := &model.Head{CgSQL: "select * from demo where sex = '${sex}'"}
	q2 := map[string][]string{"sex": {"男"}}
	if res2, err2 := QueryReport(context.Background(), db, "sqlite", head2, nil, nil, q2); err2 != nil || res2.Total != 3 {
		t.Fatalf("整体参数失败: total=%d err=%v", res2.Total, err2)
	}
	// 参数默认值（请求未传时）
	if res3, err3 := QueryReport(context.Background(), db, "sqlite", head2, nil,
		[]model.Param{{ParamName: "sex", ParamValue: "女"}}, map[string][]string{}); err3 != nil || res3.Total != 1 {
		t.Fatalf("默认值查询失败: total=%d err=%v", res3.Total, err3)
	}
	// 注入串作为参数值只能按字面匹配
	q4 := map[string][]string{"kw": {`%' OR '1'='1--`}}
	if res4, err4 := QueryReport(context.Background(), db, "sqlite", head, nil, nil, q4); err4 != nil || res4.Total != 0 {
		t.Fatalf("参数值注入未拦截: total=%d err=%v", res4.Total, err4)
	}
}

// ───── 列上筛选 ─────

// 任意已配置列支持 <col> 与 <col>_模式后缀 筛选（不依赖 isQuery 配置）。
func TestColumnFilters(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	cases := []struct {
		label string
		q     map[string][]string
		want  int
	}{
		{"裸列精确(isQuery=N的列)", map[string][]string{"sex": {"女"}}, 1},
		{"like后缀", map[string][]string{"name_like": {"王"}}, 2},
		{"in后缀", map[string][]string{"name_in": {"小张,scott"}}, 2},
		{"gt后缀", map[string][]string{"age_gt": {"18"}}, 2},
		{"ge后缀", map[string][]string{"age_ge": {"21"}}, 2},
		{"ne后缀", map[string][]string{"sex_ne": {"男"}}, 1},
		{"bw后缀", map[string][]string{"age_bw": {"18,21"}}, 2},
		{"下划线列名+bw", map[string][]string{"salary_bw": {"100,2000"}}, 2},
		{"与isQuery条件叠加", map[string][]string{"sex": {"男"}, "age_ge": {"20"}}, 1},
	}
	for _, c := range cases {
		res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, c.q)
		if err != nil {
			t.Errorf("[%s] 报错: %v", c.label, err)
			continue
		}
		if res.Total != c.want {
			t.Errorf("[%s] 期望 %d 条, 实际 %d", c.label, c.want, res.Total)
		}
	}
	// 保留参数与未知列被忽略
	res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{"no_such_col": {"x"}})
	if err != nil || res.Total != 4 {
		t.Errorf("未知列筛选应被忽略: total=%d err=%v", res.Total, err)
	}
}

// 列上筛选的注入尝试：非法列名被忽略，值走绑定参数。
func TestColumnFilterInjection(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	cases := []struct {
		label string
		q     map[string][]string
		want  int
	}{
		{"非法列名注入", map[string][]string{"name)--": {"x"}}, 4},
		{"伪造模式后缀", map[string][]string{"name_hack": {"1"}}, 4},
		{"值注入like", map[string][]string{"name_like": {"%' OR '1'='1--"}}, 0},
		// 值全部走绑定参数（无注入）；sqlite 数值列对文本区间按亲和性匹配，非null age 全中
		{"值注入bw", map[string][]string{"age_bw": {"1,999) OR (1=1"}}, 3},
		{"值注入in", map[string][]string{"name_in": {"小张) UNION SELECT 1,2,3,4,5--"}}, 0},
	}
	for _, c := range cases {
		res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, c.q)
		if err != nil {
			t.Errorf("[%s] 报错: %v", c.label, err)
			continue
		}
		if res.Total != c.want {
			t.Errorf("[%s] 期望 %d 条, 实际 %d", c.label, c.want, res.Total)
		}
	}
}

// ───── 合计列汇总 ─────

func TestSummary(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	// 全量汇总：salary = 1221.22+1133.23+99+nil = 2453.45
	res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil,
		map[string][]string{"needSummary": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary == nil {
		t.Fatal("needSummary=true 应返回 summary")
	}
	if got, ok := res.Summary["salary"].(float64); !ok || got < 2453.44 || got > 2453.46 {
		t.Errorf("salary合计错误: %v", res.Summary["salary"])
	}
	// 分页时汇总仍是全量
	res2, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil,
		map[string][]string{"needSummary": {"true"}, "pageNo": {"1"}, "pageSize": {"2"}})
	if len(res2.Records) != 2 || res2.Summary["salary"] == nil {
		t.Fatalf("分页汇总异常: %v", res2.Summary)
	}
	if res2.Summary["salary"] != res.Summary["salary"] {
		t.Errorf("汇总应为全量: %v vs %v", res2.Summary["salary"], res.Summary["salary"])
	}
	// 过滤后的汇总（age>20: 小王1 1221.22 + 小张 nil = 1221.22）
	res3, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil,
		map[string][]string{"needSummary": {"true"}, "age_gt": {"20"}})
	if got := res3.Summary["salary"].(float64); got < 1221.21 || got > 1221.23 {
		t.Errorf("age>20合计应为1221.22(小张salary为null不计入): %v", res3.Summary["salary"])
	}
	// 未开启时为空
	res4, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{})
	if res4.Summary != nil {
		t.Errorf("未传needSummary不应返回summary: %v", res4.Summary)
	}
	// 无合计列配置时为空
	items2 := testItems()
	for i := range items2 {
		items2[i].IsTotal = "N"
	}
	res5, _ := QueryReport(context.Background(), db, "sqlite", head, items2, nil, map[string][]string{"needSummary": {"true"}})
	if res5.Summary != nil {
		t.Errorf("无合计列不应返回summary: %v", res5.Summary)
	}
	// 写后失效：插入数据后汇总立即变化
	if _, err := db.Exec(`INSERT INTO demo (id, name, sex, salary) VALUES ('x1','新','女',100)`); err != nil {
		t.Fatal(err)
	}
	InvalidateCounts()
	res6, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{"needSummary": {"true"}})
	if got := res6.Summary["salary"].(float64); got < 2553.44 || got > 2553.46 {
		t.Errorf("失效后汇总应为2553.45: %v", res6.Summary["salary"])
	}
	// 缓存命中结果一致
	a, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{"needSummary": {"true"}})
	b, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil, map[string][]string{"needSummary": {"true"}})
	if a.Total != b.Total || a.Summary["salary"] != b.Summary["salary"] {
		t.Errorf("缓存命中结果不一致")
	}
}

// in 模式的值按逗号拆分后逐个绑定，不能拼进 SQL。
func TestInFilterInjection(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	items[2].QueryMode = "in"
	q := map[string][]string{"sex": {`男) OR (1=1`}}
	if res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil || res.Total != 0 {
		t.Errorf("in注入未拦截: total=%d err=%v", res.Total, err)
	}
	q = map[string][]string{"sex": {"男,女"}}
	if res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil, q); err != nil || res.Total != 4 {
		t.Errorf("in正常查询失败: total=%d err=%v", res.Total, err)
	}
}

// 公共保存：表名/列名白名单校验，注入串必须被拒绝。
func TestSaveDataValidation(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	cases := []struct {
		label string
		req   SaveRequest
	}{
		{"恶意表名", SaveRequest{Table: "demo; DROP TABLE demo", Data: map[string]any{"name": "x"}}},
		{"注释表名", SaveRequest{Table: "demo--x", Data: map[string]any{"name": "x"}}},
		{"引号表名", SaveRequest{Table: `demo"`, Data: map[string]any{"name": "x"}}},
		{"恶意列名", SaveRequest{Data: map[string]any{"name); DROP TABLE demo--": "x"}}},
		{"反引号列名", SaveRequest{Data: map[string]any{"name`": "x"}}},
	}
	for _, c := range cases {
		if _, _, err := SaveData(context.Background(), db, "sqlite", head, c.req); err == nil {
			t.Errorf("[%s] 应当拒绝", c.label)
		} else if !strings.Contains(err.Error(), "不合法") {
			t.Errorf("[%s] 错误信息异常: %v", c.label, err)
		}
	}
	// 表存在性验证：恶意保存后 demo 表应完好
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM demo").Scan(&n); err != nil || n != 4 {
		t.Fatalf("demo 表被破坏: n=%d err=%v", n, err)
	}
	// 正常保存仍可用
	saved, ids, err := SaveData(context.Background(), db, "sqlite", head, SaveRequest{Data: map[string]any{"name": "正常", "sex": "女"}})
	if err != nil || saved != 1 || len(ids[0]) != 32 {
		t.Errorf("正常保存失败: saved=%d ids=%v err=%v", saved, ids, err)
	}
	// id 值注入只能匹配 0 行
	n2, err := DeleteData(context.Background(), db, "sqlite", head, []string{`x' OR '1'='1`})
	if err != nil || n2 != 0 {
		t.Errorf("删除注入未拦截: deleted=%d err=%v", n2, err)
	}
	// 保存的 id 注入串不会命中已有行（update 路径）
	_, _, err = SaveData(context.Background(), db, "sqlite", head, SaveRequest{Data: map[string]any{"id": "demo0001' OR '1'='1", "name": "hacked"}})
	if err != nil {
		t.Errorf("注入id保存应成功但更新0行, err=%v", err)
	}
	var name string
	_ = db.QueryRow("SELECT name FROM demo WHERE id='demo0001'").Scan(&name)
	if name != "小王1" {
		t.Errorf("已有数据被注入修改: %q", name)
	}
}

// Rebind 必须跳过字符串字面量内的 "?"。
func TestRebindSkipsLiterals(t *testing.T) {
	got := dbx.Rebind("postgresql", "select * from t where a = ? and b = 'x?y' and c = ?")
	want := "select * from t where a = $1 and b = 'x?y' and c = $2"
	if got != want {
		t.Errorf("Rebind结果错误:\n got=%s\nwant=%s", got, want)
	}
	got2 := dbx.Rebind("oracle", "select 'a?b' from t where c = ?")
	want2 := "select 'a?b' from t where c = :1"
	if got2 != want2 {
		t.Errorf("Oracle Rebind结果错误:\n got=%s\nwant=%s", got2, want2)
	}
	if dbx.Rebind("mysql", "a = ? and b = ?") != "a = ? and b = ?" {
		t.Error("mysql 不应改写占位符")
	}
}

// 多语句与写操作在真实执行路径上也被拦截。
func TestQueryRejectsWriteSQL(t *testing.T) {
	db := testDB(t)
	for _, s := range []string{
		"drop table demo",
		"select 1; drop table demo",
		"insert into demo values('x','y','z',1,1)",
	} {
		head := &model.Head{CgSQL: s}
		if _, err := QueryReport(context.Background(), db, "sqlite", head, nil, nil, map[string][]string{}); err == nil {
			t.Errorf("应当拒绝: %q", s)
		}
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM demo").Scan(&n)
	if n != 4 {
		t.Errorf("demo 表数据被篡改: %d", n)
	}
}

// needCount=false 跳过 COUNT；COUNT 缓存命中结果一致。
func TestNeedCountAndCountCache(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := testItems()
	// 跳过 COUNT
	res, err := QueryReport(context.Background(), db, "sqlite", head, items, nil,
		map[string][]string{"needCount": {"false"}})
	if err != nil || res.Total != -1 || len(res.Records) != 4 {
		t.Fatalf("needCount=false 异常: total=%d n=%d err=%v", res.Total, len(res.Records), err)
	}
	// 常规查询两次（第二次命中 COUNT 缓存），结果一致
	q := map[string][]string{"name": {"王"}}
	r1, err1 := QueryReport(context.Background(), db, "sqlite", head, items, nil, q)
	r2, err2 := QueryReport(context.Background(), db, "sqlite", head, items, nil, q)
	if err1 != nil || err2 != nil || r1.Total != 2 || r2.Total != 2 {
		t.Fatalf("COUNT缓存结果不一致: %d vs %d err=%v,%v", r1.Total, r2.Total, err1, err2)
	}
	// 写后失效
	InvalidateCounts()
	r3, _ := QueryReport(context.Background(), db, "sqlite", head, items, nil, q)
	if r3.Total != 2 {
		t.Fatalf("失效后查询异常: %d", r3.Total)
	}
}

// 查询超时：context 到期后查询应被中断并返回超时错误。
func TestQueryTimeout(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 500000000) SELECT count(x) AS n FROM c"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := QueryReport(ctx, db, "sqlite", head, nil, nil, map[string][]string{})
	if err == nil {
		t.Fatal("慢查询应超时报错")
	}
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(err.Error(), "deadline") &&
		!strings.Contains(err.Error(), "interrupt") && !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("应为超时类错误: %v", err)
	}
}
