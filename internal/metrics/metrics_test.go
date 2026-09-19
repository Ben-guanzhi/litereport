package metrics

import (
	"strings"
	"testing"
)

func TestMetricsRender(t *testing.T) {
	Reset()
	ObserveHTTP("GET", "/online/cgreport/api/getData/{code}", 200, 0.125)
	ObserveHTTP("GET", "/online/cgreport/api/getData/{code}", 200, 0.375)
	ObserveHTTP("POST", "/api/ai/chat", 500, 1.5)
	ObserveQuery("demo_report", 120)
	SetPool("local", PoolStat{Open: 1, InUse: 0, Idle: 1, WaitCount: 2, WaitSeconds: 0.5})
	SetSQLiteBackup(1700000000)

	out := Render()
	for _, want := range []string{
		`litereport_http_requests_total{method="GET",pattern="/online/cgreport/api/getData/{code}",status="200"} 2`,
		`litereport_http_request_seconds_total 2`,
		`litereport_report_query_ms_total{code="demo_report"} 120`,
		`litereport_report_query_total{code="demo_report"} 1`,
		`litereport_db_pool_connections{ds="local",state="open"} 1`,
		`litereport_db_pool_wait_seconds_total{ds="local"} 0.5`,
		`litereport_sqlite_backup_timestamp 1700000000`,
		`litereport_uptime_seconds`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("指标输出缺少 %q\n%s", want, out)
		}
	}
}

// TestMetricsCountAccuracy 同 key 累加、不同 key 隔离。
func TestMetricsCountAccuracy(t *testing.T) {
	Reset()
	for i := 0; i < 3; i++ {
		ObserveQuery("a", 10)
	}
	ObserveQuery("b", 5)
	out := Render()
	if !strings.Contains(out, `litereport_report_query_ms_total{code="a"} 30`) {
		t.Errorf("a 应累计30ms:\n%s", out)
	}
	if !strings.Contains(out, `litereport_report_query_ms_total{code="b"} 5`) {
		t.Errorf("b 应为5ms:\n%s", out)
	}
}
