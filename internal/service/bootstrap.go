package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"litereport/internal/dbx"
	"litereport/internal/model"
	"litereport/internal/store"
)

// Bootstrap 幂等初始化：元数据表 → 首次启动时创建示例业务表与示例报表。
func Bootstrap(mgr *dbx.Manager) error {
	metaDB, dialect := mgr.Meta()
	ctx := context.Background()
	if err := store.EnsureTables(metaDB, dialect); err != nil {
		return err
	}
	if err := store.EnsureExtraTables(ctx, metaDB, dialect); err != nil {
		log.Printf("[bootstrap] 扩展表初始化失败: %v", err)
	}
	if err := store.RunMigrations(ctx, metaDB, dialect); err != nil {
		log.Printf("[bootstrap] schema迁移失败: %v", err)
	}
	seedDicts(metaDB, dialect)
	upgradeSeedDictCode(metaDB, dialect)
	n, err := store.HeadCount(ctx, metaDB, dialect)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	log.Printf("[bootstrap] 首次启动，正在创建示例数据 ...")
	if err := seedDemoTables(metaDB, dialect); err != nil {
		log.Printf("[bootstrap] 示例表初始化失败(不影响使用): %v", err)
		return nil
	}
	if err := seedReports(metaDB, dialect); err != nil {
		log.Printf("[bootstrap] 示例报表初始化失败(不影响使用): %v", err)
	}
	return nil
}

// seedDicts 内置字典（幂等）：log_type 日志类型。
func seedDicts(metaDB *sql.DB, dialect string) {
	ctx := context.Background()
	existing, err := store.DictList(ctx, metaDB, dialect, "log_type")
	if err == nil && len(existing) > 0 {
		return
	}
	dicts := []store.DictItem{
		{DictCode: "log_type", Label: "登录日志", Value: "1", OrderNum: 1},
		{DictCode: "log_type", Label: "操作日志", Value: "2", OrderNum: 2},
		{DictCode: "log_type", Label: "查询日志", Value: "3", OrderNum: 3},
	}
	for _, d := range dicts {
		if err := store.DictSave(ctx, metaDB, dialect, d); err != nil {
			log.Printf("[bootstrap] 字典初始化失败: %v", err)
			return
		}
	}
}

// upgradeSeedDictCode 存量升级：给示例报表 log_type 字段挂上字典。
func upgradeSeedDictCode(metaDB *sql.DB, dialect string) {
	q := `UPDATE onl_cgreport_item SET dict_code='log_type' WHERE head_id='seed-testbigdata' AND field_name='log_type' AND (dict_code='' OR dict_code IS NULL)`
	if _, err := metaDB.Exec(dbx.Rebind(dialect, q)); err != nil {
		log.Printf("[bootstrap] 字典挂载升级失败: %v", err)
	}
}
func seedDemoTables(db *sql.DB, dialect string) error {
	// dbx.DDLStatement 统一处理类型翻译 + IF NOT EXISTS / Oracle、SQLServer 存在性守卫
	for _, ddl := range []string{
		`
		CREATE TABLE demo (
			id VARCHAR(36) PRIMARY KEY,
			name VARCHAR(100),
			key_word VARCHAR(255),
			salary_money NUMERIC(12,3),
			sex VARCHAR(10),
			age INT,
			punch_time TIMESTAMP,
			bonus_money NUMERIC(12,2)
		)`,
		`
		CREATE TABLE sys_log (
			id VARCHAR(36) PRIMARY KEY,
			log_content VARCHAR(500),
			log_type VARCHAR(10),
			operate_type VARCHAR(10),
			request_param TEXT,
			request_type VARCHAR(10),
			cost_time INT,
			ip VARCHAR(100),
			user_name VARCHAR(100),
			create_by VARCHAR(64),
			create_time TIMESTAMP,
			update_by VARCHAR(64),
			update_time TIMESTAMP
		)`,
		// 常用过滤/排序列补索引（幂等），避免示例数据量增大后全表扫描
		"CREATE INDEX idx_demo_name ON demo(name)",
		"CREATE INDEX idx_demo_sex ON demo(sex)",
		"CREATE INDEX idx_syslog_ctime ON sys_log(create_time)",
		"CREATE INDEX idx_syslog_type ON sys_log(log_type)",
		"CREATE INDEX idx_syslog_user ON sys_log(user_name)",
	} {
		q, ok := dbx.DDLStatement(ddl, dialect)
		if !ok {
			continue // clickhouse 跳过普通索引
		}
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}

	var cnt int
	_ = db.QueryRow(dbx.Rebind(dialect, "SELECT COUNT(*) FROM demo")).Scan(&cnt)
	if cnt == 0 {
		demoRows := [][]any{
			{"demo0001", "小王1", "开源，很好", 1221.22, "男", 28, "1996-07-13 17:00:58", 200.00},
			{"demo0002", "scott", "开源", 1133.23, "男", 18, "2019-09-01 19:11:23", 198.50},
			{"demo0003", "小张", "", 113.033, "男", 21, "2020-02-28 14:57:20", nil},
			{"demo0004", "王五5", "", nil, "男", nil, "", nil},
			{"demo0005", "钱秀兰", "11", 33, "男", 1, "", nil},
			{"demo0006", "金军", "", 99, "男", 1, "", nil},
		}
		for _, r := range demoRows {
			_, err := db.Exec(dbx.Rebind(dialect,
				`INSERT INTO demo (id, name, key_word, salary_money, sex, age, punch_time, bonus_money)
				 VALUES (?,?,?,?,?,?,?,?)`), r...)
			if err != nil {
				return fmt.Errorf("seed demo: %w", err)
			}
		}
	}
	var cnt2 int
	_ = db.QueryRow(dbx.Rebind(dialect, "SELECT COUNT(*) FROM sys_log")).Scan(&cnt2)
	if cnt2 == 0 {
		now := time.Now()
		logs := []struct {
			content, logType, opType, reqType string
			cost                              int
			user                              string
			offset                            time.Duration
		}{
			{"用户名: admin,登录成功！", "1", "登录", "POST", 21, "admin", 0},
			{"成功删除菜单[系统监控]", "2", "删除", "DELETE", 35, "admin", 1},
			{"查询在线用户列表", "1", "查询", "GET", 12, "admin", 2},
			{"保存在线报表配置[testbigdata]", "2", "保存", "POST", 88, "admin", 3},
			{"用户名: zhangsan,登录成功！", "1", "登录", "POST", 18, "zhangsan", 4},
			{"执行SQL解析: select * from demo", "1", "查询", "POST", 54, "admin", 5},
			{"导出报表数据[在线测试报表]", "1", "导出", "GET", 132, "zhangsan", 6},
			{"更新角色权限[角色编码: admin]", "2", "更新", "PUT", 45, "admin", 7},
		}
		for i, l := range logs {
			ct := now.Add(-l.offset * 24 * time.Hour)
			_, err := db.Exec(dbx.Rebind(dialect,
				`INSERT INTO sys_log (id, log_content, log_type, operate_type, request_param, request_type,
				 cost_time, ip, user_name, create_by, create_time, update_by, update_time)
				 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`),
				fmt.Sprintf("log%04d", i), l.content, l.logType, l.opType, "{}", l.reqType,
				l.cost, "127.0.0.1", l.user, l.user, ct, l.user, ct)
			if err != nil {
				return fmt.Errorf("seed sys_log: %w", err)
			}
		}
	}
	return nil
}

func seedReports(db *sql.DB, dialect string) error {
	type rep struct {
		name, code, sqlText string
		queries             map[string]string // fieldName -> queryMode
		params              []model.Param
	}
	reps := []rep{
		{
			name: "测试大数据", code: "testbigdata",
			sqlText: "select * from sys_log order by create_time desc",
			queries: map[string]string{"log_content": "like", "log_type": "=", "user_name": "like"},
		},
		{
			name: "在线测试报表", code: "demo_report",
			sqlText: "select * from demo order by punch_time desc",
			queries: map[string]string{"name": "like", "sex": "="},
		},
		{
			name: "统计在线用户", code: "ces_app_rep001",
			sqlText: "select * from demo where sex = '${sex}'",
			queries: map[string]string{"name": "like"},
			params:  []model.Param{{ParamName: "sex", ParamTxt: "性别", ParamValue: "男"}},
		},
	}
	for _, r := range reps {
		fields, err := ParseSQL(context.Background(), db, dialect, r.sqlText)
		if err != nil {
			log.Printf("[bootstrap] 解析示例报表[%s]失败: %v", r.code, err)
			continue
		}
		head := &model.Head{
			ID:         newSeedID(r.code),
			ReportName: r.name,
			ReportCode: r.code,
			CgSQL:      r.sqlText,
			DbSource:   "local",
			CreateBy:   "admin",
		}
		items := make([]model.Item, 0, len(fields))
		for i, f := range fields {
			mode, hasQ := r.queries[f.Name]
			it := model.Item{
				FieldName: f.Name, FieldTxt: f.Name, FieldType: f.Type,
				IsShow: "Y", IsQuery: "N", QueryMode: "=", OrderNum: i + 1,
			}
			if hasQ {
				it.IsQuery = "Y"
				it.QueryMode = mode
			}
			items = append(items, it)
		}
		if err := store.HeadSave(context.Background(), db, dialect, head, items, r.params); err != nil {
			return err
		}
	}
	log.Printf("[bootstrap] 已创建 %d 个示例报表", len(reps))
	return nil
}

func newSeedID(code string) string {
	return "seed-" + code
}
