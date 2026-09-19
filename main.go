// LiteReport —— 在线报表配置平台
// 后端 Go，前端纯 HTML，元数据与业务数据源支持 sqlite/mysql/postgresql/oracle。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"litereport/internal/api"
	"litereport/internal/auth"
	"litereport/internal/config"
	"litereport/internal/dbx"
	"litereport/internal/metrics"
	"litereport/internal/service"
	"litereport/internal/store"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	pool := dbx.PoolSpec{
		MaxOpen:     cfg.DbPool.MaxOpen,
		MaxIdle:     cfg.DbPool.MaxIdle,
		MaxLifetime: time.Duration(cfg.DbPool.MaxLifetimeMinutes) * time.Minute,
		MaxIdleTime: time.Duration(cfg.DbPool.MaxIdleTimeMinutes) * time.Minute,
	}
	metaDB, err := dbx.OpenWithPool(cfg.Meta.Type, cfg.Meta.DSN, pool)
	if err != nil {
		log.Fatalf("打开元数据库失败: %v", err)
	}
	dialect := dbx.Normalize(cfg.Meta.Type)
	log.Printf("元数据库: %s (%s)", cfg.Meta.DSN, dialect)

	dsCfgs := make([]dbx.DSConfig, 0, len(cfg.Datasources))
	for _, d := range cfg.Datasources {
		dsCfgs = append(dsCfgs, dbx.DSConfig{Key: d.Key, Name: d.Name, Type: d.Type, DSN: d.DSN})
	}
	mgr := dbx.NewManagerWithPool(metaDB, dialect, dsCfgs, pool)

	if err := service.Bootstrap(mgr); err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	loadDBDatasources(mgr, metaDB, dialect, cfg.Auth.Secret)

	// 数据保留清理 + 元库滚动备份：启动时执行一次，之后每 24h 一次（retention 段可配，0 为永久保留）
	go func() {
		backup := func() {
			if dialect != dbx.SQLite {
				return
			}
			dir := "./data/backups"
			_ = os.MkdirAll(dir, 0o755)
			target := filepath.Join(dir, fmt.Sprintf("litereport-%s.db", time.Now().Format("20060102-150405")))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := store.SQLiteBackupPath(ctx, metaDB, target); err != nil {
				slog.Error("元库备份失败", "err", err)
				return
			}
			metrics.SetSQLiteBackup(time.Now().Unix())
			slog.Info("元库已备份", "path", target)
			// 只保留最近 7 份
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			var dbs []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
					dbs = append(dbs, filepath.Join(dir, e.Name()))
				}
			}
			sort.Strings(dbs)
			for i := 0; i < len(dbs)-7; i++ {
				_ = os.Remove(dbs[i])
			}
		}
		run := func() {
			backup()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := store.CleanupRetention(ctx, metaDB, dialect,
				cfg.Retention.AuditDays, cfg.Retention.SQLHistoryDays, cfg.Retention.VersionDays, cfg.Retention.ChatDays); err != nil {
				slog.Error("数据保留清理失败", "err", err)
			}
		}
		run()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()

	// 定时报表推送调度器（按报表配置的数据源取数；SMTP 未配置时邮件渠道跳过并记录日志）
	stopPush := service.StartPushScheduler(mgr, service.SmtpConfig{
		Host:       cfg.Smtp.Host,
		Port:       cfg.Smtp.Port,
		Username:   cfg.Smtp.Username,
		Password:   cfg.Smtp.Password,
		From:       cfg.Smtp.From,
		AlertEmail: cfg.Smtp.AlertEmail,
	})

	srv := api.NewServer(cfg, mgr, "web")
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("LiteReport 平台已启动: http://localhost:%d", cfg.Server.Port)
	log.Printf("公共查询接口示例: http://localhost:%d/online/cgreport/api/getData/demo_report", cfg.Server.Port)

	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("启动失败: %v", err)
		}
	}()

	// 优雅停机：等待信号 → 10s 内排空连接 → 关闭所有连接
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("收到退出信号，优雅停机中")
	stopPush() // 等待进行中的一轮推送扫描结束
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	// 关闭所有数据源连接池
	if mgr != nil {
		mgr.CloseAll()
	}
	if d := metaDB; d != nil {
		_ = d.Close()
	}
	slog.Info("已退出")
}

// loadDBDatasources 把元库中保存的动态数据源注册进连接管理器。
func loadDBDatasources(mgr *dbx.Manager, metaDB *sql.DB, dialect, secret string) {
	ctx := context.Background()
	rows, err := store.DSList(ctx, metaDB, dialect)
	if err != nil {
		return
	}
	for _, r := range rows {
		dsn := auth.DecryptDSN(r.DSN, secret)
		if dsn == "" {
			slog.Warn("数据源 DSN 解密失败", "key", r.Key)
			continue
		}
		if err := mgr.Upsert(dbx.DSConfig{Key: r.Key, Name: r.Name, Type: r.Type, DSN: dsn, Source: "db"}); err != nil {
			slog.Warn("数据源注册失败", "key", r.Key, "err", err)
		}
	}
	if len(rows) > 0 {
		log.Printf("[datasource] 已从元库加载 %d 个动态数据源", len(rows))
	}
}
