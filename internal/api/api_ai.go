// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"litereport/internal/auth"

	"litereport/internal/model"
	"litereport/internal/service"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── AI 生成 SQL ─────

// aiMessagesOf 解析并限制对话消息体（条数与单条长度），同时返回会话标识。
func aiMessagesOf(w http.ResponseWriter, r *http.Request) ([]service.ChatMessage, string) {
	var body struct {
		SessionID string                `json:"sessionId"`
		Messages  []service.ChatMessage `json:"messages"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Messages) == 0 {
		model.Err(w, "对话内容不能为空")
		return nil, ""
	}
	body.SessionID = strings.TrimSpace(body.SessionID)
	if body.SessionID == "" || len(body.SessionID) > 40 {
		body.SessionID = store.NewID()
	}
	if len(body.Messages) > 30 {
		body.Messages = body.Messages[len(body.Messages)-30:]
	}
	for i := range body.Messages {
		if len([]rune(body.Messages[i].Content)) > 8000 {
			body.Messages[i].Content = string([]rune(body.Messages[i].Content)[:8000])
		}
	}
	return body.Messages, body.SessionID
}

// handleAIChatStream AI助手流式对话（SSE）：逐增量下发，完成后持久化对话。
func (s *Server) handleAIChatStream(w http.ResponseWriter, r *http.Request) {
	msgs, sessionID := aiMessagesOf(w, r)
	if msgs == nil {
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if fl != nil {
			fl.Flush()
		}
	}
	ai := s.aiCfg()
	full, err := service.AIChatStream(r.Context(), service.AiConfig{
		BaseURL: ai.BaseURL, APIKey: ai.APIKey, Model: ai.Model,
	}, msgs, func(delta string) {
		b, _ := json.Marshal(map[string]string{"delta": delta})
		send(string(b))
	})
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		send(string(b))
		return
	}
	// 持久化本轮对话（用户最后一条提问 + 助手完整回复）
	metaDB, dialect := s.mgr.Meta()
	user := currentUser(r.Context())
	bg := context.Background()
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		_ = store.ChatInsert(bg, metaDB, dialect, user, sessionID, "user", msgs[len(msgs)-1].Content)
	}
	_ = store.ChatInsert(bg, metaDB, dialect, user, sessionID, "assistant", full)
	b, _ := json.Marshal(map[string]any{"done": true, "sessionId": sessionID})
	send(string(b))
}

// handleAIHistory 当前用户指定会话的历史对话（最近100条，升序；session 缺省=legacy）。
func (s *Server) handleAIHistory(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	session := r.URL.Query().Get("session")
	if session == "" {
		session = "legacy"
	}
	list, err := store.ChatList(r.Context(), metaDB, dialect, currentUser(r.Context()), session, 100)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

// handleAIHistoryClear 清空对话（?session= 指定会话；缺省=legacy；session=all 清全部）。
func (s *Server) handleAIHistoryClear(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	session := r.URL.Query().Get("session")
	if session == "all" {
		session = ""
	} else if session == "" {
		session = "legacy"
	}
	if err := store.ChatClear(r.Context(), metaDB, dialect, currentUser(r.Context()), session); err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, nil)
}

// handleAISessions 当前用户的会话列表（最近50个，按活跃倒序）。
func (s *Server) handleAISessions(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.ChatSessions(r.Context(), metaDB, dialect, currentUser(r.Context()))
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

// handleAIConfigGet 查看 AI 配置（admin）：密钥不回传，只报是否已配置。
func (s *Server) handleAIConfigGet(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	c, err := store.AIGetConfig(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	if c != nil {
		model.OK(w, map[string]any{
			"baseUrl": c.BaseURL, "model": c.Model, "hasKey": c.APIKey != "",
			"updateBy": c.UpdateBy, "updateTime": c.UpdateTime, "source": "db",
		})
		return
	}
	y := s.aiConf
	model.OK(w, map[string]any{
		"baseUrl": y.BaseURL, "model": y.Model, "hasKey": y.APIKey != "",
		"source": "config",
	})
}

// handleAIConfigSave 保存 AI 配置（admin）：密钥 AES-GCM 加密入库，留空沿用原密钥。
func (s *Server) handleAIConfigSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	}
	if err := readJSON(r, &body); err != nil {
		model.Err(w, "请求体解析失败")
		return
	}
	body.BaseURL = strings.TrimSpace(body.BaseURL)
	body.Model = strings.TrimSpace(body.Model)
	if body.BaseURL != "" && !strings.HasPrefix(body.BaseURL, "http://") && !strings.HasPrefix(body.BaseURL, "https://") {
		model.Err(w, "base_url 需以 http(s):// 开头")
		return
	}
	apiKeyEnc := ""
	if body.APIKey = strings.TrimSpace(body.APIKey); body.APIKey != "" {
		apiKeyEnc = auth.EncryptDSN(body.APIKey, s.auth.SecretString())
	} else {
		metaDB, dialect := s.mgr.Meta()
		if c, err := store.AIGetConfig(r.Context(), metaDB, dialect); err == nil && c != nil {
			apiKeyEnc = c.APIKey
		}
	}
	metaDB, dialect := s.mgr.Meta()
	user := currentUser(r.Context())
	if err := store.AISaveConfig(r.Context(), metaDB, dialect, body.BaseURL, apiKeyEnc, body.Model, user); err != nil {
		model.Err(w, "保存失败: "+err.Error())
		return
	}
	s.audit(r, "AI配置保存", body.BaseURL, body.Model)
	model.OK(w, nil)
}

// handleAIChat AI助手多轮对话（页面 /ai.html）。
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []service.ChatMessage `json:"messages"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Messages) == 0 {
		model.Err(w, "对话内容不能为空")
		return
	}
	// 限制上下文长度与单条消息长度，避免请求体过大 / 消耗过多上游配额
	if len(body.Messages) > 30 {
		body.Messages = body.Messages[len(body.Messages)-30:]
	}
	for i := range body.Messages {
		if len([]rune(body.Messages[i].Content)) > 8000 {
			body.Messages[i].Content = string([]rune(body.Messages[i].Content)[:8000])
		}
	}
	ai := s.aiCfg()
	reply, err := service.AIChat(r.Context(), service.AiConfig{
		BaseURL: ai.BaseURL, APIKey: ai.APIKey, Model: ai.Model,
	}, body.Messages)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, map[string]any{"reply": reply})
}

func (s *Server) handleAISQL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Desc     string `json:"desc"`
		Table    string `json:"table"`
		DbSource string `json:"dbSource"`
	}
	if err := readJSON(r, &body); err != nil || body.Desc == "" || body.Table == "" {
		model.Err(w, "请填写需求描述与表名")
		return
	}
	ai := s.aiCfg()
	target, dialect, err := s.mgr.Get(body.DbSource)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	fields, err := service.ParseSQL(r.Context(), target, dialect, "select * from "+body.Table)
	if err != nil {
		model.Err(w, "读取表结构失败: "+err.Error())
		return
	}
	sqlText, err := service.GenerateSQL(r.Context(), service.AiConfig{
		BaseURL: ai.BaseURL, APIKey: ai.APIKey, Model: ai.Model,
	}, body.Desc, body.Table, fields)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "AI生成SQL", body.Table, body.Desc)
	model.OK(w, map[string]any{"sql": sqlText})
}
