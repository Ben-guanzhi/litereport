// 报表分享链接：按报表生成带有效期的只读访问令牌。
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"litereport/internal/model"
	"litereport/internal/store"
)

// shareReadActions 分享链接允许的只读动作（白名单，写操作一律拒绝）。
var shareReadActions = map[string]bool{
	"getData": true, "getDataNoPage": true, "getColumns": true, "getInfo": true,
	"getDict": true, "getChartData": true, "getCrossTable": true,
	"exportExcel": true, "exportJson": true, "exportSQLInsert": true,
}

// validShare 校验分享链接：GET + 只读动作白名单 + token 与报表匹配且未过期。
// 校验结果缓存 60s（撤销后最长 1 分钟生效）。
func (s *Server) validShare(r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/online/cgreport/api/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[1] == "" || !shareReadActions[parts[0]] {
		return false
	}
	return s.validShareTokenFor(r.Context(), parts[1], r.URL.Query().Get("share"))
}

// validShareTokenFor 校验报表编码对应的分享令牌（结果缓存 60s，撤销后最长 1 分钟生效）。
func (s *Server) validShareTokenFor(ctx context.Context, code, token string) bool {
	if code == "" || token == "" {
		return false
	}
	key := "share:" + code + ":" + token
	if v, ok := s.cfgCache.Get(key); ok {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	ok := false
	if _, _, pack, err := s.loadReport(ctx, code); err == nil {
		h := pack.head
		if h.ShareToken != "" && h.ShareToken == token {
			if h.ShareExpire == "" || shareNotExpired(h.ShareExpire) {
				ok = true
			}
		}
	}
	s.cfgCache.Set(key, ok, 60*time.Second)
	return ok
}

func shareNotExpired(s string) bool {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	return err == nil && t.After(time.Now())
}

// handleHeadShare POST /online/cgreport/head-share/{id}  body {days}（0=永久）
// 生成分享令牌，返回免登录只读访问链接。
func (s *Server) handleHeadShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	metaDB, dialect := s.mgr.Meta()
	head, err := store.HeadGet(r.Context(), metaDB, dialect, id)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	var body struct {
		Days int `json:"days"`
	}
	_ = readJSON(r, &body)
	token := store.NewID()
	var expire *time.Time
	if body.Days > 0 {
		t := time.Now().AddDate(0, 0, body.Days)
		expire = &t
	}
	if err := store.HeadSetShare(r.Context(), metaDB, dialect, head.ID, token, expire); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.invalidateCfgCache() // 配置缓存中的 head 带旧分享状态，必须失效
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	url := scheme + "://" + r.Host + "/online/cgreport/" + head.ReportCode + "?share=" + token
	expireText := "永久"
	if expire != nil {
		expireText = expire.Format("2006-01-02 15:04")
	}
	s.audit(r, "生成分享链接", head.ReportCode, expireText)
	model.OK(w, map[string]any{"url": url, "token": token, "expire": expireText})
}

// handleHeadShareRevoke DELETE /online/cgreport/head-share/{id} 撤销分享。
func (s *Server) handleHeadShareRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	metaDB, dialect := s.mgr.Meta()
	head, err := store.HeadGet(r.Context(), metaDB, dialect, id)
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	if err := store.HeadSetShare(r.Context(), metaDB, dialect, head.ID, "", nil); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.invalidateCfgCache() // 立即生效
	s.cfgCache.DeletePrefix("share:")
	s.audit(r, "撤销分享链接", head.ReportCode, "")
	model.OK(w, nil)
}
