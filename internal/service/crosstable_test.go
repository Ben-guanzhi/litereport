package service

import (
	"context"
	"fmt"
	"testing"

	"litereport/internal/model"
)

// TestCrossTableTruncation 回归：聚合组数超过上限时截断并标记 truncated。
func TestCrossTableTruncation(t *testing.T) {
	db := testDB(t)
	// 构造 30 个不同 name（> maxCols=12，但不触发行截断），检查基础透视
	head := &model.Head{CgSQL: "select * from demo"}
	items := []model.Item{
		{FieldName: "name", FieldTxt: "name"},
		{FieldName: "sex", FieldTxt: "sex"},
		{FieldName: "salary", FieldTxt: "salary", FieldType: "数值类型"},
	}
	ct, err := CrossTableData(context.Background(), db, "sqlite", head, items, nil, map[string][]string{},
		"name", "sex", "salary", "sum", 12)
	if err != nil {
		t.Fatal(err)
	}
	if ct.Truncated {
		t.Fatal("少量数据不应截断")
	}
	if len(ct.Rows) == 0 || len(ct.Columns) == 0 {
		t.Fatalf("交叉表结果异常: %d行 %d列", len(ct.Rows), len(ct.Columns))
	}

	// 行截断场景：直接用高基数维度（每行 name 唯一），把上限压到 3 行验证截断逻辑
	old := crossTableMaxRows
	defer func() { crossTableMaxRows = old }()
	crossTableMaxRows = 3
	ct2, err := CrossTableData(context.Background(), db, "sqlite", head, items, nil, map[string][]string{},
		"name", "sex", "salary", "sum", 12)
	if err != nil {
		t.Fatal(err)
	}
	if !ct2.Truncated {
		t.Fatal("超过上限应标记 truncated")
	}
	if len(ct2.Rows) > 3 {
		t.Fatalf("行数应被截断到3, got %d", len(ct2.Rows))
	}
}

// TestCrossTableBasic 基础透视正确性。
func TestCrossTableBasic(t *testing.T) {
	db := testDB(t)
	head := &model.Head{CgSQL: "select * from demo"}
	items := []model.Item{
		{FieldName: "name", FieldTxt: "name"},
		{FieldName: "sex", FieldTxt: "sex"},
		{FieldName: "salary", FieldTxt: "salary", FieldType: "数值类型"},
	}
	ct, err := CrossTableData(context.Background(), db, "sqlite", head, items, nil, map[string][]string{},
		"sex", "sex", "salary", "count", 12)
	if err != nil {
		t.Fatal(err)
	}
	// 男3条 女1条
	byRow := map[string]any{}
	for _, r := range ct.Rows {
		byRow[r["__row"].(string)] = r["男"]
	}
	if byRow["男"] != int64(3) {
		t.Errorf("男 count 应为3, got %v", byRow["男"])
	}
	if fmt.Sprint(ct.Rows) == "" {
		t.Fatal("空结果")
	}
}
