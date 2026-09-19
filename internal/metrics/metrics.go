// Package metrics 进程内指标收集与 Prometheus 文本格式渲染（零外部依赖）。
// 覆盖：HTTP 请求计数/耗时、报表查询耗时、数据库连接池状态。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type counter struct {
	count float64
	sum   float64
}

var (
	mu       sync.Mutex
	httpReqs = map[string]*counter{} // key: method|pattern|status
	queryMs  = map[string]*counter{} // key: reportCode
	startAt  = time.Now()
)

func get(m map[string]*counter, key string) *counter {
	c, ok := m[key]
	if !ok {
		c = &counter{}
		m[key] = c
	}
	return c
}

// ObserveHTTP 记录一次 HTTP 请求（pattern 用 ServeMux 路由模板，避免路径基数爆炸）。
func ObserveHTTP(method, pattern string, status int, seconds float64) {
	if pattern == "" {
		pattern = "unmatched"
	}
	mu.Lock()
	defer mu.Unlock()
	c := get(httpReqs, method+"|"+pattern+"|"+fmt.Sprint(status))
	c.count++
	c.sum += seconds
}

// ObserveQuery 记录一次报表查询耗时（毫秒）。
func ObserveQuery(code string, ms float64) {
	mu.Lock()
	defer mu.Unlock()
	c := get(queryMs, code)
	c.count++
	c.sum += ms
}

// SetSQLiteBackup 记录最近一次元库备份时间戳（unix 秒，0=从未）。
var lastBackup int64

func SetSQLiteBackup(ts int64) {
	mu.Lock()
	lastBackup = ts
	mu.Unlock()
}

// PoolStat 单个数据源连接池状态快照。
type PoolStat struct {
	Open        int64
	InUse       int64
	Idle        int64
	WaitCount   int64
	WaitSeconds float64
	MaxOpen     int64
}

var (
	poolMu    sync.Mutex
	poolStats = map[string]PoolStat{}
)

// SetPool 更新数据源连接池状态（/metrics 抓取时渲染）。
func SetPool(ds string, st PoolStat) {
	poolMu.Lock()
	poolStats[ds] = st
	poolMu.Unlock()
}

func f(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}

// Reset 清空全部指标（测试用）。
func Reset() {
	mu.Lock()
	httpReqs = map[string]*counter{}
	queryMs = map[string]*counter{}
	lastBackup = 0
	mu.Unlock()
	poolMu.Lock()
	poolStats = map[string]PoolStat{}
	poolMu.Unlock()
	startAt = time.Now()
}

// Render 输出 Prometheus 文本格式。
func Render() string {
	var b strings.Builder
	mu.Lock()
	keys := make([]string, 0, len(httpReqs))
	totalCount, totalSum := 0.0, 0.0
	for k := range httpReqs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("# HELP litereport_http_requests_total HTTP 请求计数。\n# TYPE litereport_http_requests_total counter\n")
	for _, k := range keys {
		c := httpReqs[k]
		parts := strings.SplitN(k, "|", 3)
		b.WriteString(fmt.Sprintf("litereport_http_requests_total{method=%q,pattern=%q,status=%q} %s\n",
			parts[0], parts[1], parts[2], f(c.count)))
		totalCount += c.count
		totalSum += c.sum
	}
	b.WriteString("# HELP litereport_http_request_seconds_total HTTP 请求累计耗时。\n# TYPE litereport_http_request_seconds_total counter\n")
	b.WriteString(fmt.Sprintf("litereport_http_request_seconds_total %s\n", f(totalSum)))
	b.WriteString("# HELP litereport_report_query_ms_total 报表查询累计耗时(毫秒)。\n# TYPE litereport_report_query_ms_total counter\n")
	qkeys := make([]string, 0, len(queryMs))
	for k := range queryMs {
		qkeys = append(qkeys, k)
	}
	sort.Strings(qkeys)
	for _, k := range qkeys {
		c := queryMs[k]
		b.WriteString(fmt.Sprintf("litereport_report_query_ms_total{code=%q} %s\n", k, f(c.sum)))
		b.WriteString(fmt.Sprintf("litereport_report_query_total{code=%q} %s\n", k, f(c.count)))
	}
	bak := lastBackup
	mu.Unlock()

	poolMu.Lock()
	pkeys := make([]string, 0, len(poolStats))
	for k := range poolStats {
		pkeys = append(pkeys, k)
	}
	sort.Strings(pkeys)
	b.WriteString("# HELP litereport_db_pool_connections 数据源连接池连接数。\n# TYPE litereport_db_pool_connections gauge\n")
	for _, k := range pkeys {
		p := poolStats[k]
		ds := fmt.Sprintf("ds=%q", k)
		b.WriteString(fmt.Sprintf("litereport_db_pool_connections{%s,state=%q} %s\n", ds, "open", f(float64(p.Open))))
		b.WriteString(fmt.Sprintf("litereport_db_pool_connections{%s,state=%q} %s\n", ds, "in_use", f(float64(p.InUse))))
		b.WriteString(fmt.Sprintf("litereport_db_pool_connections{%s,state=%q} %s\n", ds, "idle", f(float64(p.Idle))))
	}
	b.WriteString("# HELP litereport_db_pool_wait_seconds_total 数据源连接池累计等待秒数。\n# TYPE litereport_db_pool_wait_seconds_total counter\n")
	for _, k := range pkeys {
		b.WriteString(fmt.Sprintf("litereport_db_pool_wait_seconds_total{ds=%q} %s\n", k, f(poolStats[k].WaitSeconds)))
	}
	poolMu.Unlock()

	b.WriteString("# HELP litereport_uptime_seconds 进程运行秒数。\n# TYPE litereport_uptime_seconds gauge\n")
	b.WriteString(fmt.Sprintf("litereport_uptime_seconds %s\n", f(time.Since(startAt).Seconds())))
	b.WriteString("# HELP litereport_sqlite_backup_timestamp 最近一次元库备份时间。\n# TYPE litereport_sqlite_backup_timestamp gauge\n")
	b.WriteString(fmt.Sprintf("litereport_sqlite_backup_timestamp %d\n", bak))
	return b.String()
}
