// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"litereport/internal/model"
	"litereport/internal/store"
)

func (s *Server) registerExtraRoutes() {
	// ───── 数据源管理（管理端）─────
	s.mux.HandleFunc("GET /api/datasource/all", s.handleDSListFull)
	s.mux.HandleFunc("POST /api/datasource/save", s.requireRole(s.handleDSSave, "admin"))
	s.mux.HandleFunc("POST /api/datasource/test", s.requireRole(s.handleDSTest, "editor"))
	s.mux.HandleFunc("DELETE /api/datasource/{key}", s.requireRole(s.handleDSDelete, "admin"))

	// ───── 字典（管理端 CRUD + 公共查询）─────
	s.mux.HandleFunc("GET /api/dict/list", s.handleDictList)
	s.mux.HandleFunc("POST /api/dict/save", s.requireRole(s.handleDictSave, "editor"))
	s.mux.HandleFunc("DELETE /api/dict/{id}", s.requireRole(s.handleDictDelete, "editor"))
	s.mux.HandleFunc("GET /online/cgreport/api/getDict/{code}", s.handleGetDict)

	// ───── Excel 导出（公共接口）─────
	s.mux.HandleFunc("GET /online/cgreport/api/exportExcel/{code}", s.handleExportExcel)

	// ───── 审计日志（管理端）─────
	s.mux.HandleFunc("GET /api/audit/list", s.requireRole(s.handleAuditList, "admin"))

	// ───── 元库全量备份（管理端）─────
	s.mux.HandleFunc("GET /api/backup/export", s.requireRole(s.handleBackupExport, "admin"))
	s.mux.HandleFunc("POST /api/backup/restore", s.requireRole(s.handleBackupRestore, "admin"))

	// ───── 配置版本（管理端）─────
	s.mux.HandleFunc("GET /online/cgreport/head-versions", s.handleVersionList)
	s.mux.HandleFunc("GET /online/cgreport/head-version/{vid}", s.handleVersionGet)
	s.mux.HandleFunc("POST /online/cgreport/head-version/{vid}/rollback", s.requireRole(s.handleVersionRollback, "editor"))
	s.mux.HandleFunc("GET /online/cgreport/head-export/{id}", s.handleHeadExport)
	s.mux.HandleFunc("POST /online/cgreport/head/import", s.requireRole(s.handleHeadImport, "editor"))
	s.mux.HandleFunc("POST /online/cgreport/head-share/{id}", s.requireRole(s.handleHeadShare, "editor"))
	s.mux.HandleFunc("DELETE /online/cgreport/head-share/{id}", s.requireRole(s.handleHeadShareRevoke, "editor"))

	// ───── 图表（管理端 CRUD + 公共数据）─────
	s.mux.HandleFunc("GET /api/chart/list", s.handleChartList)
	s.mux.HandleFunc("POST /api/chart/save", s.requireRole(s.handleChartSave, "editor"))
	s.mux.HandleFunc("DELETE /api/chart/{id}", s.requireRole(s.handleChartDelete, "editor"))
	s.mux.HandleFunc("GET /online/cgreport/api/getChartData/{code}", s.handleChartData)

	// ───── 表单开发（管理端）─────
	s.mux.HandleFunc("GET /online/cgform/list", s.handleFormList)
	s.mux.HandleFunc("POST /online/cgform/create", s.requireRole(s.handleFormCreate, "editor"))
	s.mux.HandleFunc("DELETE /online/cgform/{id}", s.requireRole(s.handleFormDelete, "editor"))

	// ───── 定时推送（管理端）─────
	s.mux.HandleFunc("GET /api/push/list", s.handlePushList)
	s.mux.HandleFunc("POST /api/push/save", s.requireRole(s.handlePushSave, "admin"))
	s.mux.HandleFunc("POST /api/push/send/{id}", s.requireRole(s.handlePushSend, "admin"))
	s.mux.HandleFunc("DELETE /api/push/{id}", s.requireRole(s.handlePushDelete, "admin"))

	// ───── AI（editor+：消耗外部 API 配额，不对 viewer 开放）─────
	s.mux.HandleFunc("POST /api/ai/sql", s.requireRole(s.handleAISQL, "editor"))
	s.mux.HandleFunc("POST /api/ai/chat", s.requireRole(s.handleAIChat, "editor"))
	s.mux.HandleFunc("POST /api/ai/chat/stream", s.requireRole(s.handleAIChatStream, "editor"))
	s.mux.HandleFunc("GET /api/ai/history", s.requireRole(s.handleAIHistory, "editor"))
	s.mux.HandleFunc("DELETE /api/ai/history", s.requireRole(s.handleAIHistoryClear, "editor"))
	s.mux.HandleFunc("GET /api/ai/sessions", s.requireRole(s.handleAISessions, "editor"))
	s.mux.HandleFunc("GET /api/ai/config", s.requireRole(s.handleAIConfigGet, "admin"))
	s.mux.HandleFunc("POST /api/ai/config", s.requireRole(s.handleAIConfigSave, "admin"))
}

// audit 记录审计日志（异步，失败不影响主流程）。
// audit 记录审计日志：写入带缓冲队列（容量1024），单 worker 串行落库——
// 避免突发流量下每个事件各起 goroutine 直接挤兑 SQLite 单连接；队列满时丢弃并记日志。
func (s *Server) audit(r *http.Request, action, target, detail string) {
	user := currentUser(r.Context())
	if user == "" {
		user = "anonymous"
	}
	entry := store.AuditRow{
		UserName: user, IP: s.clientIP(r), Action: action, Target: target, Detail: detail,
	}
	select {
	case s.auditCh <- entry:
	default:
		slog.Warn("审计队列已满，丢弃", "action", action, "target", target)
	}
}

// auditWorker 审计落库消费者（NewServer 启动，随进程生命周期）。
func (s *Server) auditWorker() {
	for entry := range s.auditCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		metaDB, dialect := s.mgr.Meta()
		if err := store.AuditInsert(ctx, metaDB, dialect, entry); err != nil {
			slog.Error("审计写入失败", "err", err)
		}
		cancel()
	}
}

// ───── 审计日志 ─────

func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	q := r.URL.Query()
	page, size := atoi(q.Get("pageNo")), atoi(q.Get("pageSize"))
	list, total, err := store.AuditList(r.Context(), metaDB, dialect, q.Get("action"), q.Get("user"), page, size)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	model.OK(w, model.Page{Records: list, Total: total, Size: size, Current: page, Pages: (total + size - 1) / size})
}
