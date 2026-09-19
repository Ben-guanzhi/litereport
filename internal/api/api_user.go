// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"net/http"
	"strings"

	"litereport/internal/auth"

	"litereport/internal/model"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 用户管理（admin）─────

func (s *Server) registerUserRoutes() {
	s.mux.HandleFunc("GET /api/users", s.requireRole(s.handleUserList, "admin"))
	s.mux.HandleFunc("POST /api/users/save", s.requireRole(s.handleUserSave, "admin"))
	s.mux.HandleFunc("DELETE /api/users/{name}", s.requireRole(s.handleUserDelete, "admin"))
	s.mux.HandleFunc("GET /api/sqlhistory", s.requireRole(s.handleSQLHistory, "editor"))
}

// handleUserList GET /api/users —— config 用户 + 平台用户表合并视图。
func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request) {
	out := []store.UserRow{}
	for _, u := range s.auth.ConfigUsers() {
		out = append(out, store.UserRow{Name: u.Name, Role: u.Role, Source: "config"})
	}
	metaDB, dialect := s.mgr.Meta()
	dbUsers, err := store.UserListDB(r.Context(), metaDB, dialect)
	if err == nil {
		out = append(out, dbUsers...)
	}
	model.OK(w, out)
}

// handleUserSave POST /api/users/save {name, password?, role}
// name 不存在则创建（需密码），存在则更新角色/重置密码（DB 用户）。
func (s *Server) handleUserSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" {
		model.Err(w, "用户名不能为空")
		return
	}
	role := strings.ToLower(body.Role)
	if role != "admin" && role != "editor" && role != "viewer" {
		role = "editor"
	}
	// config.yaml 定义的用户由配置文件管理，页面建同名 DB 用户会遮蔽它（登录查 DB 优先）
	for _, cu := range s.auth.ConfigUsers() {
		if cu.Name == body.Name {
			model.Err(w, "用户["+body.Name+"]由 config.yaml 管理，请在配置文件中修改后重启")
			return
		}
	}
	metaDB, dialect := s.mgr.Meta()
	ctx := r.Context()
	var existsHash string
	h0, _, _, err0 := store.UserAuthInfo(ctx, metaDB, dialect, body.Name)
	existsHash = h0
	_ = err0
	if existsHash == "" {
		// 新用户：必须有初始密码
		if len(body.Password) < 6 {
			model.Err(w, "新用户密码长度至少6位")
			return
		}
		if err := store.UserSetPasswordTx(ctx, metaDB, dialect, body.Name, auth.HashPassword(body.Password), role); err != nil {
			model.Err(w, err.Error())
			return
		}
	} else {
		// 已存在：带密码则重置+更新角色（同事务）；否则仅更新角色
		if body.Password != "" {
			if len(body.Password) < 6 {
				model.Err(w, "密码长度至少6位")
				return
			}
			if err := store.UserSetPasswordTx(ctx, metaDB, dialect, body.Name, auth.HashPassword(body.Password), role); err != nil {
				model.Err(w, err.Error())
				return
			}
		} else {
			if err := store.UserRole(ctx, metaDB, dialect, body.Name, role); err != nil {
				model.Err(w, err.Error())
				return
			}
		}
	}
	verCache.Delete("ver:" + body.Name)
	s.audit(r, "用户保存", body.Name, role)
	model.OK(w, map[string]any{"name": body.Name, "role": role})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != "" && name == currentUser(r.Context()) {
		model.Err(w, "不能删除当前登录的自己")
		return
	}
	metaDB, dialect := s.mgr.Meta()
	// 平台必须至少保留 1 个 admin（DB 用户 + config 用户合计）
	adminLeft := 0
	for _, cu := range s.auth.ConfigUsers() {
		if cu.Name != name && strings.EqualFold(cu.Role, "admin") {
			adminLeft++
		}
	}
	rows, err := store.UserListDB(r.Context(), metaDB, dialect)
	if err == nil {
		for _, u := range rows {
			if u.Name != name && strings.EqualFold(u.Role, "admin") {
				adminLeft++
			}
		}
	}
	if adminLeft == 0 {
		model.Err(w, "删除后将没有任何 admin 用户，请先把其他用户提升为 admin")
		return
	}
	if err := store.UserDeleteDB(r.Context(), metaDB, dialect, name); err != nil {
		model.Err(w, err.Error())
		return
	}
	verCache.Delete("ver:" + name)
	s.audit(r, "用户删除", name, "")
	model.OK(w, nil)
}
