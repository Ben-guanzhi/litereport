package dbx

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

// PoolSpec 连接池参数（零值使用默认值）。
type PoolSpec struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// Open 按默认连接池参数打开数据库连接。
func Open(dialect, dsn string) (*sql.DB, error) {
	return OpenWithPool(dialect, dsn, PoolSpec{})
}

// OpenWithPool 打开数据库连接并调优：
//   - mysql：默认开启 interpolateParams（值全部走绑定参数，客户端插值省一次
//     服务端 prepare 往返），DSN 已显式设置时不覆盖
//   - sqlite：单连接 + WAL + busy_timeout，避免锁冲突并提升读并发
func OpenWithPool(dialect, dsn string, pool PoolSpec) (*sql.DB, error) {
	dialect = Normalize(dialect)
	driver := DriverName(dialect)
	if driver == "sqlite" && !strings.Contains(dsn, ":") && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	if driver == "mysql" {
		dsn = tuneMySQLDSN(dsn)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		// sqlite 单写者：限制连接避免 database is locked
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := tuneSQLite(db); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}
	if pool.MaxOpen > 0 {
		db.SetMaxOpenConns(pool.MaxOpen)
	} else {
		db.SetMaxOpenConns(20)
	}
	if pool.MaxIdle > 0 {
		db.SetMaxIdleConns(pool.MaxIdle)
	} else {
		db.SetMaxIdleConns(10)
	}
	if pool.MaxLifetime > 0 {
		db.SetConnMaxLifetime(pool.MaxLifetime)
	} else {
		db.SetConnMaxLifetime(time.Hour)
	}
	if pool.MaxIdleTime > 0 {
		db.SetConnMaxIdleTime(pool.MaxIdleTime)
	} else {
		db.SetConnMaxIdleTime(10 * time.Minute)
	}
	return db, nil
}

// tuneMySQLDSN 默认开启 interpolateParams：平台所有查询值均为绑定参数，
// 客户端插值可省去每次执行的服务端 prepare 往返。
func tuneMySQLDSN(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return dsn // 非标准 DSN 原样返回，交给驱动报错
	}
	cfg.InterpolateParams = true
	return cfg.FormatDSN()
}

// tuneSQLite 启用 WAL 与忙等待，减少锁冲突。
func tuneSQLite(db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("sqlite 调优失败(%s): %w", pragma, err)
		}
	}
	return nil
}

// DSConfig 数据源配置视图。
type DSConfig struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	DSN    string `json:"-"`
	Source string `json:"source,omitempty"` // config / db
}

// DSView 数据源状态视图（返回给前端）。
type DSView struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"` // online / offline
	Source string `json:"source"` // config / db
}

// Manager 管理元数据库连接与动态数据源连接池。
type Manager struct {
	mu          sync.RWMutex
	metaDB      *sql.DB
	metaDialect string
	pools       map[string]*sql.DB
	dialects    map[string]string
	cfgs        []DSConfig
	pool        PoolSpec
	listCache   []DSView // List 结果缓存（listTTL）
	listAt      time.Time
	closeOnce   map[string]*sync.Once // 用于安全关闭数据库连接
}

func NewManager(metaDB *sql.DB, metaDialect string, ds []DSConfig) *Manager {
	return NewManagerWithPool(metaDB, metaDialect, ds, PoolSpec{})
}

func NewManagerWithPool(metaDB *sql.DB, metaDialect string, ds []DSConfig, pool PoolSpec) *Manager {
	m := &Manager{
		metaDB:      metaDB,
		metaDialect: metaDialect,
		pools:       map[string]*sql.DB{},
		dialects:    map[string]string{},
		cfgs:        append([]DSConfig{}, ds...),
		pool:        pool,
		closeOnce:   make(map[string]*sync.Once),
	}
	m.pools["local"] = metaDB
	m.dialects["local"] = metaDialect
	return m
}

// Get 取数据源连接（local 指向元数据库），返回连接与方言。
func (m *Manager) Get(key string) (*sql.DB, string, error) {
	if key == "" {
		key = "local"
	}
	m.mu.RLock()
	db, ok := m.pools[key]
	dl := m.dialects[key]
	m.mu.RUnlock()
	if ok {
		return db, dl, nil
	}
	var cfg *DSConfig
	m.mu.RLock()
	for i := range m.cfgs {
		if m.cfgs[i].Key == key {
			cfg = &m.cfgs[i]
			break
		}
	}
	m.mu.RUnlock()
	if cfg == nil {
		return nil, "", fmt.Errorf("数据源[%s]不存在", key)
	}
	ndb, err := OpenWithPool(cfg.Type, cfg.DSN, m.pool)
	if err != nil {
		return nil, "", fmt.Errorf("打开数据源[%s]失败: %w", key, err)
	}
	if err := ndb.Ping(); err != nil {
		ndb.Close()
		return nil, "", fmt.Errorf("连接数据源[%s]失败: %w", key, err)
	}
	dl = Normalize(cfg.Type)
	m.mu.Lock()
	m.pools[key], m.dialects[key] = ndb, dl
	m.mu.Unlock()
	return ndb, dl, nil
}

// listTTL 数据源在线状态缓存时长：List 每次对全量数据源做真实 Ping（可能各等一个
// connect 超时），加短 TTL 避免页面刷新时双倍串行等待；Upsert/Remove 时主动失效。
const listTTL = 15 * time.Second

// List 列出全部数据源及在线状态（带 listTTL 缓存）。
func (m *Manager) List() []DSView {
	m.mu.RLock()
	if time.Since(m.listAt) < listTTL && m.listCache != nil {
		out := m.listCache
		m.mu.RUnlock()
		return out
	}
	m.mu.RUnlock()
	cfgs := m.cfgsSnapshot()
	out := make([]DSView, 0, len(cfgs))
	for _, c := range cfgs {
		v := DSView{Key: c.Key, Name: c.Name, Type: Normalize(c.Type), Status: "offline", Source: c.Source}
		if db, _, err := m.Get(c.Key); err == nil && db.Ping() == nil {
			v.Status = "online"
		}
		out = append(out, v)
	}
	m.mu.Lock()
	m.listCache, m.listAt = out, time.Now()
	m.mu.Unlock()
	return out
}

// cfgsSnapshot 配置列表拷贝（加锁）。
func (m *Manager) cfgsSnapshot() []DSConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]DSConfig{}, m.cfgs...)
}

// Upsert 新增/更新动态数据源（页面化管理），立即建立并校验连接。
func (m *Manager) Upsert(c DSConfig) error {
	c.Type = Normalize(c.Type)
	if c.Key == "" || !ValidIdent(strings.Split(c.Key, ".")[0]) || c.Key == "local" {
		return fmt.Errorf("数据源key不合法或与内置冲突")
	}
	db, err := OpenWithPool(c.Type, c.DSN, m.pool)
	if err != nil {
		return fmt.Errorf("打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("连接失败: %w", err)
	}
	m.mu.Lock()
	replaced := m.pools[c.Key]
	m.pools[c.Key], m.dialects[c.Key] = db, c.Type
	m.listAt = time.Time{} // 失效 List 缓存
	found := false
	for i := range m.cfgs {
		if m.cfgs[i].Key == c.Key {
			m.cfgs[i] = c
			found = true
			break
		}
	}
	if !found {
		m.cfgs = append(m.cfgs, c)
	}
	m.mu.Unlock()
	if replaced != nil && replaced != m.metaDB {
		go replaced.Close() // 旧连接池延迟关闭
	}
	return nil
}

// Remove 移除动态数据源连接池（不删除元库记录，由调用方处理）。
func (m *Manager) Remove(key string) {
	if key == "local" {
		return
	}
	m.mu.Lock()
	db, existed := m.pools[key]
	if existed {
		m.pools[key] = nil // 清空引用，防止后续使用
		delete(m.pools, key)
		delete(m.dialects, key)
		m.listAt = time.Time{} // 失效 List 缓存
		// 移除配置记录
		for i := range m.cfgs {
			if m.cfgs[i].Key == key {
				m.cfgs = append(m.cfgs[:i], m.cfgs[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	// 安全关闭数据库连接（使用 sync.Once 确保仅关闭一次）
	if db != nil && db != m.metaDB {
		m.closeOnce[key] = &sync.Once{}
		m.closeOnce[key].Do(func() {
			db.Close()
		})
	}
}

// TestConnection 测试指定配置能否连通。
func TestConnection(dsType, dsn string) error {
	db, err := Open(dsType, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return err
	}
	return nil
}

// Meta 返回元数据库连接与方言。
func (m *Manager) Meta() (*sql.DB, string) { return m.metaDB, m.metaDialect }

// Stats 返回全部已建立连接池的 sql.DBStats（按数据源 key）。
func (m *Manager) Stats() map[string]sql.DBStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]sql.DBStats, len(m.pools))
	for k, db := range m.pools {
		out[k] = db.Stats()
	}
	return out
}

// CloseAll 关闭所有已建立的数据源连接池（不关闭元数据库）。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, db := range m.pools {
		if db != m.metaDB {
			db.Close()
		}
	}
	m.pools = map[string]*sql.DB{"local": m.metaDB}
	m.dialects = map[string]string{"local": m.metaDialect}
	m.listAt = time.Time{}
}
