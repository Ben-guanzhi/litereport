// Package api HTTP 路由与处理器：公共在线报表接口 + 报表配置管理接口 + 静态页面。
// 中间件链：panic恢复 → CORS → 限流 → 分级鉴权（管理端会话 / 公共接口Token）。
package api

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"litereport/internal/auth"
	"litereport/internal/cache"
	"litereport/internal/config"
	"litereport/internal/dbx"
	"litereport/internal/metrics"
	"litereport/internal/model"
	"litereport/internal/ratelimit"
	"litereport/internal/store"
)

type Server struct {
	mux          *http.ServeMux
	mgr          *dbx.Manager
	webRoot      string
	cfgCache     *cache.Cache // 报表配置缓存（保存/删除时失效）
	auth         *auth.Manager
	authEnabled  bool
	sessionHours int
	aiConf       config.AiCfg
	smtpConf     config.SmtpCfg
	queryTimeout time.Duration
	trustedXFF   map[string]bool // 可信反向代理 IP（直连对端命中才信任 X-Forwarded-For）
	auditCh      chan store.AuditRow
	limPublic    ratelimit.AllowFunc
	limAdmin     ratelimit.AllowFunc
	limLogin     ratelimit.AllowFunc
}

func NewServer(cfg *config.Config, mgr *dbx.Manager, webRoot string) *Server {
	users := make([]auth.User, 0, len(cfg.Auth.Users))
	for _, u := range cfg.Auth.Users {
		users = append(users, auth.User{Name: u.Name, Password: u.Password, Role: u.Role})
	}
	xff := map[string]bool{}
	for _, ip := range cfg.Security.TrustedProxies {
		if ip = strings.TrimSpace(ip); ip != "" {
			xff[ip] = true
		}
	}
	s := &Server{
		mux:          http.NewServeMux(),
		mgr:          mgr,
		webRoot:      webRoot,
		cfgCache:     cache.New(),
		auth:         auth.NewManager(cfg.Auth.Secret, cfg.Auth.SessionHours, users, cfg.Auth.APITokens),
		authEnabled:  cfg.AuthEnabled(),
		sessionHours: cfg.Auth.SessionHours,
		aiConf:       cfg.Ai,
		smtpConf:     cfg.Smtp,
		queryTimeout: time.Duration(cfg.Query.TimeoutSeconds) * time.Second,
		trustedXFF:   xff,
		auditCh:      make(chan store.AuditRow, 1024),
	}
	// 限流：配置 redis_addr 时多实例共享窗口，否则内存令牌桶
	rlMgr := ratelimit.NewManager()
	if cfg.RateLimit.RedisAddr != "" {
		if err := rlMgr.EnableRedis(cfg.RateLimit.RedisAddr, cfg.RateLimit.RedisPass, cfg.RateLimit.RedisDB); err != nil {
			slog.Warn("redis 限流不可用，回退内存", "err", err)
		} else {
			slog.Info("redis 共享限流已启用", "addr", cfg.RateLimit.RedisAddr)
		}
	}
	s.limPublic = rlMgr.Bucket("public", cfg.RateLimit.PublicRPS, cfg.RateLimit.PublicBurst, int(cfg.RateLimit.PublicRPS)*60)
	s.limAdmin = rlMgr.Bucket("admin", cfg.RateLimit.AdminRPS, cfg.RateLimit.AdminBurst, int(cfg.RateLimit.AdminRPS)*60)
	s.limLogin = rlMgr.Bucket("login", cfg.RateLimit.LoginPerMin/60, 5, int(cfg.RateLimit.LoginPerMin))
	if cfg.Auth.Secret == "" {
		log.Printf("[auth] 未配置 auth.secret，使用内置默认签名密钥（生产环境请在 config.yaml 中修改）")
	}
	if !s.authEnabled {
		log.Printf("[auth] 鉴权已关闭（auth.enabled=false），所有接口匿名可访问")
	}
	s.auth.SetUserLookup(func(name string) (string, int, string, error) {
		metaDB, dialect := mgr.Meta()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hash, ver, role, err := store.UserAuthInfo(ctx, metaDB, dialect, name)
		if err != nil {
			return "", 0, "", err
		}
		return hash, ver, role, nil
	})
	go s.auditWorker()
	// 连接池状态定期同步到 /metrics
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			mgr.List() // 顺带刷新数据源在线状态缓存（离线源恢复后 ≤15s 自动转在线）
			for ds, st := range mgr.Stats() {
				metrics.SetPool(ds, metrics.PoolStat{
					Open: int64(st.OpenConnections), InUse: int64(st.InUse), Idle: int64(st.Idle),
					WaitCount: st.WaitCount, WaitSeconds: st.WaitDuration.Seconds(), MaxOpen: int64(st.MaxOpenConnections),
				})
			}
		}
	}()
	s.routes()
	return s
}

// handleMetrics Prometheus 文本格式指标（admin 会话 或 X-API-Token）。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(metrics.Render()))
}

// handleVersion 构建信息（登录后可查）：版本号与运行时长。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	model.OK(w, map[string]any{
		"version":       Version,
		"uptimeSeconds": int(time.Since(processStart).Seconds()),
	})
}

// invalidateCfgCache 报表配置变更后清除全部配置缓存。
func (s *Server) invalidateCfgCache() { s.cfgCache.DeletePrefix("cfg:") }

// headPack 报表配置聚合。
type headPack struct {
	head   *model.Head
	items  []model.Item
	params []model.Param
}

// loadReport 按 code(或 id) 加载报表配置并打开目标数据源。
// 配置带 5 分钟缓存（保存/删除时主动失效），公共查询接口无需每次查元库 3 张表。
func (s *Server) loadReport(ctx context.Context, key string) (*sql.DB, string, *headPack, error) {
	if v, ok := s.cfgCache.Get("cfg:" + key); ok {
		if pack, ok2 := v.(*headPack); ok2 {
			target, tdialect, err := s.mgr.Get(pack.head.DbSource)
			if err != nil {
				return nil, "", nil, err
			}
			return target, tdialect, pack, nil
		}
	}
	metaDB, dialect := s.mgr.Meta()
	head, err := store.HeadFindByKey(ctx, metaDB, dialect, key)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, "", nil, notFoundErr("报表不存在: " + key)
		}
		return nil, "", nil, err
	}
	items, err := store.ItemsByHead(ctx, metaDB, dialect, head.ID)
	if err != nil {
		return nil, "", nil, err
	}
	params, err := store.ParamsByHead(ctx, metaDB, dialect, head.ID)
	if err != nil {
		return nil, "", nil, err
	}
	pack := &headPack{head: head, items: items, params: params}
	s.cfgCache.Set("cfg:"+key, pack, 5*time.Minute)
	target, tdialect, err := s.mgr.Get(head.DbSource)
	if err != nil {
		return nil, "", nil, err
	}
	return target, tdialect, pack, nil
}

type notFoundErr string

func (e notFoundErr) Error() string { return string(e) }

func (s *Server) routes() {
	// ───── 公共在线报表接口（供第三方系统 / 前端页面调用）─────
	s.mux.HandleFunc("GET /online/cgreport/api/getData/{code}", s.handleGetData)
	s.mux.HandleFunc("GET /online/cgreport/api/getDataNoPage/{code}", s.handleGetDataNoPage)
	s.mux.HandleFunc("GET /online/cgreport/api/getColumns/{code}", s.handleGetColumns)
	s.mux.HandleFunc("GET /online/cgreport/api/getInfo/{key}", s.handleGetInfo)
	s.mux.HandleFunc("POST /online/cgreport/api/saveData/{code}", s.handleSaveData)
	s.mux.HandleFunc("PUT /online/cgreport/api/saveData/{code}", s.handleSaveData)
	s.mux.HandleFunc("POST /online/cgreport/api/importExcel/{code}", s.handleImportExcel)
	s.mux.HandleFunc("POST /online/cgreport/api/deleteData/{code}", s.handleDeleteData)
	s.mux.HandleFunc("DELETE /online/cgreport/api/deleteData/{code}", s.handleDeleteData)

	// ───── 报表配置管理接口 ─────
	s.mux.HandleFunc("GET /online/cgreport/head/list", s.handleHeadList)
	s.mux.HandleFunc("GET /online/cgreport/head/{id}", s.handleHeadGet)
	s.mux.HandleFunc("POST /online/cgreport/head", s.requireRole(s.handleHeadSave, "editor"))
	s.mux.HandleFunc("PUT /online/cgreport/head", s.requireRole(s.handleHeadSave, "editor"))
	s.mux.HandleFunc("DELETE /online/cgreport/head/{id}", s.requireRole(s.handleHeadDelete, "editor"))
	s.mux.HandleFunc("POST /online/cgreport/sql/parse", s.requireRole(s.handleSQLParse, "editor"))
	s.mux.HandleFunc("GET /api/datasource/list", s.handleDatasourceList)

	// ───── 健康检查（免鉴权）─────
	s.mux.HandleFunc("GET /healthz", s.handleHealth)

	// ───── 指标（鉴权中间件已允许 admin 会话 或 API Token，无需重复门卫）─────
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)

	// ───── pprof 性能剖析（admin）─────
	s.mux.HandleFunc("GET /debug/pprof/", s.requireRole(pprof.Index, "admin"))
	s.mux.HandleFunc("GET /debug/pprof/cmdline", s.requireRole(pprof.Cmdline, "admin"))
	s.mux.HandleFunc("GET /debug/pprof/profile", s.requireRole(pprof.Profile, "admin"))
	s.mux.HandleFunc("GET /debug/pprof/symbol", s.requireRole(pprof.Symbol, "admin"))
	s.mux.HandleFunc("GET /debug/pprof/trace", s.requireRole(pprof.Trace, "admin"))
	s.mux.HandleFunc("GET /api/version", s.handleVersion)

	// ───── 鉴权 ─────
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.handleMe)
	s.mux.HandleFunc("POST /api/auth/password", s.handlePasswordChange)

	// ───── P1/P2 扩展路由 ─────
	s.registerExtraRoutes()
	s.registerExtraRoutes2()
	s.registerUserRoutes()
	s.mux.HandleFunc("GET /online/cgreport/api/exportJson/{code}", s.handleExportJSON)
	s.mux.HandleFunc("GET /online/cgreport/api/exportSQLInsert/{code}", s.handleExportSQLInsert)

	// ───── 页面路由：功能测试跳转 AUTO在线报表 ─────
	s.mux.HandleFunc("GET /online/cgreport/{key}", s.handleCgreportPage)

	// ───── 静态资源 ─────
	s.mux.HandleFunc("GET /", s.handleStatic)
}

// ───── 中间件：分级鉴权 + 限流 ─────

// 免鉴权路径：登录/登出、登录页、静态资源
func isAuthFreePath(p string) bool {
	return p == "/api/auth/login" || p == "/api/auth/logout" || p == "/login.html" ||
		strings.HasPrefix(p, "/css/") || strings.HasPrefix(p, "/js/") || strings.HasPrefix(p, "/lib/") || p == "/favicon.ico" || p == "/healthz"
}

// clientIP 提取客户端 IP。默认直连部署取 RemoteAddr；仅当直连对端是
// security.trusted_proxies 配置的可信代理时才信任 X-Forwarded-For 首段，
// 防止直连场景伪造 XFF 绕过限流、污染审计日志。
func (s *Server) clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]") // IPv6 字面量
	if s.trustedXFF[host] {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.SplitN(xf, ",", 2)[0])
		}
	}
	return host
}

// safeNext 限制登录后的站内跳转目标：仅允许以单个 "/" 开头的相对路径，防开放重定向。
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

type userKey struct{}

// currentUser 取当前登录用户名（鉴权中间件注入）。
func currentUser(ctx context.Context) string {
	if v, ok := ctx.Value(userKey{}).(string); ok {
		return v
	}
	return ""
}

const sessionCookie = "litrpt_session"

// verCache 会话版本缓存（30s），避免每请求查库
var verCache = cache.New()

// sessionUser 从 Cookie / Authorization Bearer 解析会话用户（校验会话版本）。
func (s *Server) sessionUser(r *http.Request) (string, string) {
	user, ver, role, err := s.parseToken(r)
	if err != nil || user == "" {
		return "", ""
	}
	if cv := s.currentVer(user); cv != ver {
		return "", "" // 密码已修改或被强制下线
	}
	return user, role
}

// parseToken 解出令牌中的用户与版本。
func (s *Server) parseToken(r *http.Request) (string, int, string, error) {
	tok := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		tok = c.Value
	}
	if tok == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if tok == "" {
		return "", 0, "", auth.ErrInvalidToken
	}
	return s.auth.Verify(tok)
}

// currentVer 取用户当前会话版本（DB用户查库，配置用户恒为0，带30s缓存）。
func (s *Server) currentVer(user string) int {
	key := "ver:" + user
	if v, ok := verCache.Get(key); ok {
		if n, ok2 := v.(int); ok2 {
			return n
		}
	}
	ver := 0
	metaDB, dialect := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if v, err := store.UserTokenVersion(ctx, metaDB, dialect, user); err == nil {
		ver = v
	}
	verCache.Set(key, ver, 30*time.Second)
	return ver
}

func apiTokenOf(r *http.Request) string {
	if h := r.Header.Get("X-API-Token"); h != "" {
		return h
	}
	return r.URL.Query().Get("token")
}

// authMiddleware 分级鉴权：
//   - 管理端（页面/配置接口/SQL解析）：需要登录会话
//   - 公共接口 /online/cgreport/api/*：会话 或 API Token（X-API-Token 头 / token 参数）
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if isAuthFreePath(p) {
			next.ServeHTTP(w, r)
			return
		}
		if user, role := s.sessionUser(r); user != "" {
			ctx := context.WithValue(r.Context(), userKey{}, user)
			ctx = context.WithValue(ctx, roleKey{}, role)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// 公共接口放行：API Token（/metrics 供监控抓取同样放行）
		if (strings.HasPrefix(p, "/online/cgreport/api/") || p == "/metrics") && s.auth.CheckAPIToken(apiTokenOf(r)) {
			next.ServeHTTP(w, r)
			return
		}
		// 报表分享链接：GET + 只读动作白名单 + 有效 token，免登录放行
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/online/cgreport/api/") && s.validShare(r) {
			next.ServeHTTP(w, r)
			return
		}
		// 分享页面本身免登录（AUTO在线报表 HTML，数据请求由前端带 share 参数）
		if r.Method == http.MethodGet && strings.HasPrefix(p, "/online/cgreport/") && !strings.HasPrefix(p, "/online/cgreport/api/") {
			code := strings.Trim(strings.TrimPrefix(p, "/online/cgreport/"), "/")
			if code != "" && !strings.Contains(code, "/") && s.validShareTokenFor(r.Context(), code, r.URL.Query().Get("share")) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if wantsHTML(r) {
			http.Redirect(w, r, "/login.html?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusFound)
			return
		}
		model.ErrStatus(w, http.StatusUnauthorized, "未登录或令牌无效")
	})
}

// rateLimitMiddleware 令牌桶限流（按客户端 IP 分桶）。
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		lim := s.limAdmin
		if isAuthFreePath(p) || strings.HasPrefix(p, "/online/cgreport/api/") {
			lim = s.limPublic
		}
		if !lim(s.clientIP(r)) {
			model.ErrStatus(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxBodyBytes 全局 JSON 请求体上限（10MB）：导入配置批量数据均远小于此值。
const maxBodyBytes = 10 << 20

// corsMiddleware 跨域处理：
//   - 公共接口 /online/cgreport/api/* 下发通配源（第三方带 API Token 跨域调用）；
//   - 管理端 /api/* 同源使用，不下发通配源（避免未来放开凭据时成为漏洞面）；
//   - OPTIONS 预检在鉴权/限流之前短路返回 204，未登录的跨域预检不再被 401 拦截。
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 基础安全响应头
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if strings.HasPrefix(r.URL.Path, "/online/cgreport/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Token, X-Requested-With")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Version 构建版本号：CI 通过 -ldflags "-X litereport/internal/api.Version=xxx" 注入。
var Version = "dev"

var processStart = time.Now()

// statusWriter 记录响应状态码（供日志与指标使用）。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

// observeMiddleware 指标采集 + 请求日志：
//   - 全部请求计入 /metrics（path 用路由模板，避免路径基数爆炸）；
//   - 日志策略：公共报表 API 与管理端 API 全记录；静态资源仅慢(>200ms)或非2xx时记录。
func (s *Server) observeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		dur := time.Since(start)

		p := r.URL.Path
		isStatic := strings.HasPrefix(p, "/css/") || strings.HasPrefix(p, "/js/") ||
			strings.HasPrefix(p, "/lib/") || p == "/favicon.ico"
		isReportPage := strings.HasPrefix(p, "/online/cgreport/") && !strings.HasPrefix(p, "/online/cgreport/api/")
		if isStatic || isReportPage {
			// 静态资源/页面：降噪，仅慢或异常时记录
			if dur > 200*time.Millisecond || sw.status >= 400 {
				slog.Warn("http slow or error", "method", r.Method, "path", p, "status", sw.status, "duration_ms", dur.Milliseconds(), "ip", s.clientIP(r))
			}
			return
		}
		slog.Info("http", "method", r.Method, "path", p, "status", sw.status, "duration_ms", dur.Milliseconds(), "ip", s.clientIP(r), "bytes", sw.bytes)
	})
}

// Handler 中间件链：CORS/预检 → 指标/日志 → panic恢复 → 限流 → 分级鉴权 → 路由。
func (s *Server) Handler() http.Handler {
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "method", r.Method, "path", r.URL.Path, "err", fmt.Sprint(rec))
				model.ErrStatus(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		// 统一限制请求体大小，防止超大 JSON 打满内存
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		// 指标在最内层采集：ServeMux 在此 request 上写入路由模板 r.Pattern
		// （外层中间件的 WithContext 克隆体会丢失 Pattern）
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		if acceptsGzip(r) {
			w.Header().Add("Vary", "Accept-Encoding")
			gw := &gzipResponseWriter{ResponseWriter: sw}
			s.mux.ServeHTTP(gw, r)
			gw.finish() // 刷出 gzip 尾部
		} else {
			s.mux.ServeHTTP(sw, r)
		}
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		metrics.ObserveHTTP(r.Method, r.Pattern, sw.status, time.Since(start).Seconds())
	})
	return s.corsMiddleware(s.observeMiddleware(s.authMiddleware(s.rateLimitMiddleware(s.timeoutMiddleware(root)))))
}

// handleLogin 登录：校验用户名密码，签发 HttpOnly 会话 Cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limLogin(s.clientIP(r)) {
		model.ErrStatus(w, http.StatusTooManyRequests, "登录尝试过于频繁，请稍后再试")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		model.Err(w, "请求体解析失败")
		return
	}
	user, role, _, err := s.auth.VerifyPassword(body.Username, body.Password)
	if err != nil {
		model.ErrStatus(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	ver := s.currentVer(user)
	tok, err := s.auth.Sign(user, ver, role)
	if err != nil {
		model.Err(w, "签发会话失败")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.sessionHours * 3600,
	})
	slog.Info("用户登录", "user", user, "ip", s.clientIP(r))
	s.audit(r, "登录", user, "")
	model.OK(w, map[string]any{"username": user})
}

// handleLogout 退出：清除会话 Cookie。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	model.OK(w, nil)
}

// handleMe 当前登录用户。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, role := s.sessionUser(r)
	if user == "" {
		model.ErrStatus(w, http.StatusUnauthorized, "未登录")
		return
	}
	model.OK(w, map[string]any{"username": user, "role": role})
}

// ───── gzip 响应压缩（JSON/HTML/CSS/JS 等，降低报表数据传输体积）─────

var compressiblePrefixes = []string{
	"application/json", "application/javascript", "application/xml",
	"text/", "image/svg+xml",
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(strings.SplitN(part, ";", 2)[0]) == "gzip" {
			return true
		}
	}
	return false
}

func shouldCompress(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if ct == "" || ct == "text/event-stream" { // SSE 流式响应不压缩，保证逐块下发
		return false
	}
	for _, p := range compressiblePrefixes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz           *gzip.Writer
	wroteHeader  bool
	compressible bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.wroteHeader = true
	if code >= 200 && code < 300 && shouldCompress(g.Header().Get("Content-Type")) {
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
		g.compressible = true
		g.gz = gzip.NewWriter(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.compressible {
		n, err := g.gz.Write(p)
		if err == nil {
			err = g.gz.Flush()
		}
		return n, err
	}
	return g.ResponseWriter.Write(p)
}

// Flush 透传刷新：gzip 场景先刷 gzip 缓冲再刷底层连接（SSE 逐块下发依赖此方法）。
func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Finish 收尾：把 gzip 尾部刷到底层连接。
func (g *gzipResponseWriter) finish() {
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz = nil
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// HTML/CSS/JS 入口不做浏览器缓存（no-cache 仍走 304 协商），避免页面更新后前端拿到旧脚本
	if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") ||
		strings.HasPrefix(r.URL.Path, "/css/") || strings.HasPrefix(r.URL.Path, "/js/") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// 默认首页为个人工作台；Online报表配置页仍可通过 /index.html 访问
	// （http.ServeFile 会把 /index.html 301 到 ./，故这里直接读文件返回）
	if r.URL.Path == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		serveHTMLFile(w, r, filepath.Join(s.webRoot, "index.html"))
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "" {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(s.webRoot, "workspace.html"))
		return
	}
	// 纵深防御：拒绝目录穿越路径（net/http 的 ServeMux/ServeFile 已有防护）
	if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "\x00") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.webRoot, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	http.NotFound(w, r)
}

// handleCgreportPage 提供 /online/cgreport/{id} 在线报表访问地址（AUTO在线报表页面）。
func (s *Server) handleCgreportPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(s.webRoot, "cgreport.html"))
}

// serveHTMLFile 直接读取 HTML 文件返回（绕开 http.ServeFile 对 /index.html 的 301 重定向）。
func serveHTMLFile(w http.ResponseWriter, r *http.Request, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	return dec.Decode(v)
}

// timeoutMiddleware 为报表/数据链路请求附加统一查询超时（query.timeout_seconds），
// 慢 SQL 到期后由驱动中断，避免占死连接池。
// /api/ai/* 豁免：AI 模型响应耗时较长，走 chatCompletion 自身的 120s 客户端超时。
func (s *Server) timeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.queryTimeout > 0 && !strings.HasPrefix(r.URL.Path, "/api/ai/") &&
			(strings.HasPrefix(r.URL.Path, "/online/cgreport/") || strings.HasPrefix(r.URL.Path, "/api/")) {
			ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// aiCfg 解析生效的 AI 配置：页面化配置（元库 ai_config）优先，yaml 作兜底默认。
func (s *Server) aiCfg() config.AiCfg {
	metaDB, dialect := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := store.AIGetConfig(ctx, metaDB, dialect); err == nil && c != nil {
		return config.AiCfg{
			BaseURL: c.BaseURL,
			APIKey:  auth.DecryptDSN(c.APIKey, s.auth.SecretString()),
			Model:   c.Model,
		}
	}
	return s.aiConf
}

func (s *Server) smtpCfg() config.SmtpCfg { return s.smtpConf }

// handleHealth 就绪探针：元库可 ping 即健康。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	metaDB, _ := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := metaDB.PingContext(ctx); err != nil {
		model.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "down", "error": err.Error()})
		return
	}
	model.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "time": time.Now().Format("2006-01-02 15:04:05"), "version": Version,
	})
}

// handlePasswordChange 修改密码：校验旧密码 → 存 bcrypt 哈希 → 会话版本+1（强制重新登录）。
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())
	if user == "" {
		model.ErrStatus(w, http.StatusUnauthorized, "未登录")
		return
	}
	var body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := readJSON(r, &body); err != nil || body.OldPassword == "" || body.NewPassword == "" {
		model.Err(w, "请提供旧密码与新密码")
		return
	}
	if len(body.NewPassword) < 6 {
		model.Err(w, "新密码长度至少6位")
		return
	}
	if _, _, _, err := s.auth.VerifyPassword(user, body.OldPassword); err != nil {
		model.Err(w, "旧密码错误")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := store.UserSetPassword(ctx, metaDB, dialect, user, auth.HashPassword(body.NewPassword), currentUserRole(r.Context())); err != nil {
		model.Err(w, "保存失败: "+err.Error())
		return
	}
	verCache.Delete("ver:" + user)
	s.audit(r, "修改密码", user, "")
	model.OK(w, map[string]any{"relogin": true})
}

type roleKey struct{}

// currentUserRole 取当前用户角色（admin/editor/viewer；未登录为空）。
func currentUserRole(ctx context.Context) string {
	if v, ok := ctx.Value(roleKey{}).(string); ok {
		return v
	}
	return ""
}

// requireRole 角色门卫：viewer 不可过 editorOnly；仅 admin 可过 adminOnly。
func (s *Server) requireRole(h http.HandlerFunc, minRole string) http.HandlerFunc {
	rank := map[string]int{"viewer": 1, "editor": 2, "admin": 3}
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled {
			h(w, r)
			return
		}
		role := currentUserRole(r.Context())
		if role == "" {
			role = "viewer"
		}
		if rank[role] < rank[minRole] {
			model.ErrStatus(w, http.StatusForbidden, "权限不足（需要"+minRole+"角色）")
			return
		}
		h(w, r)
	}
}
