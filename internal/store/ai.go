// AI 深化存储：对话历史持久化与页面化 AI 配置。
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"litereport/internal/dbx"
)

// ───── AI 对话历史 ─────

type ChatMsg struct {
	ID         string `json:"id"`
	UserName   string `json:"userName"`
	Role       string `json:"role"` // user / assistant
	Content    string `json:"content"`
	CreateTime string `json:"createTime"`
}

// ChatInsert 追加一条对话记录。
func ChatInsert(ctx context.Context, db *sql.DB, dialect string, userName, session, role, content string) error {
	// seq 取当前最大值+1，保证同秒插入的稳定排序
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO ai_chat_history (id, user_name, session_id, role, content, create_time, seq)
		 VALUES (?,?,?,?,?,?,(SELECT COALESCE(MAX(seq),0)+1 FROM ai_chat_history))`),
		NewID(), userName, session, role, content, time.Now())
	return err
}

// ChatList 查询某用户最近 N 条对话（按时间升序返回，供回放）。
func ChatList(ctx context.Context, db *sql.DB, dialect, userName, session string, limit int) ([]ChatMsg, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := dbx.Rebind(dialect, dbx.PageWrap(dialect,
		"SELECT id, user_name, role, content, create_time FROM ai_chat_history WHERE user_name=? AND session_id=? ORDER BY seq DESC", 1, limit))
	rows, err := db.QueryContext(ctx, q, userName, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMsg{}
	for rows.Next() {
		var m ChatMsg
		var ct sql.NullTime
		if err := rows.Scan(&m.ID, &m.UserName, &m.Role, &m.Content, &ct); err != nil {
			return nil, err
		}
		m.CreateTime = FmtNullTime(ct)
		out = append(out, m)
	}
	// 翻转为升序（PageWrap 取的是最新 limit 条）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ChatClear 清空某用户对话历史（session 为空 = 全部）。
func ChatClear(ctx context.Context, db *sql.DB, dialect, userName, session string) error {
	q := `DELETE FROM ai_chat_history WHERE user_name=?`
	args := []any{userName}
	if session != "" {
		q += ` AND session_id=?`
		args = append(args, session)
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect, q), args...)
	return err
}

// ChatSession 会话列表项。
type ChatSession struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
	LastTime  string `json:"lastTime"`
}

// ChatSessions 某用户的会话列表（按最近活跃倒序，标题取首条提问，限50个）。
func ChatSessions(ctx context.Context, db *sql.DB, dialect, userName string) ([]ChatSession, error) {
	q := dbx.Rebind(dialect,
		`SELECT session_id, MAX(create_time), COUNT(*) FROM ai_chat_history WHERE user_name=? GROUP BY session_id ORDER BY MAX(create_time) DESC`)
	rows, err := db.QueryContext(ctx, q, userName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatSession{}
	for rows.Next() {
		var cs ChatSession
		var sid sql.NullString
		var last any // sqlite 把时间存为文本, 驱动类型不定
		if err := rows.Scan(&sid, &last, &cs.Count); err != nil {
			return nil, err
		}
		cs.SessionID = sid.String
		switch t := last.(type) {
		case time.Time:
			cs.LastTime = t.Format("2006-01-02 15:04:05")
		case string:
			cs.LastTime = t
		case []byte:
			cs.LastTime = string(t)
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 标题：会话内首条用户提问（截断30字）
	for i := range out {
		var title string
		tq := dbx.Rebind(dialect, dbx.LimitWrapN(dialect,
			"SELECT content FROM ai_chat_history WHERE user_name=? AND session_id=? AND role='user' ORDER BY seq ASC", 1))
		if err := db.QueryRowContext(ctx, tq, userName, out[i].SessionID).Scan(&title); err == nil {
			r := []rune(strings.TrimSpace(title))
			if len(r) > 30 {
				r = r[:30]
			}
			out[i].Title = string(r)
		}
		if out[i].Title == "" {
			out[i].Title = "(无标题会话)"
		}
	}
	return out, nil
}

// ───── AI 配置（页面化，yaml 为兜底默认）─────

type AIConfig struct {
	BaseURL    string `json:"baseUrl"`
	APIKey     string `json:"-"` // 密文存储，导出视图单独处理
	Model      string `json:"model"`
	UpdateBy   string `json:"updateBy"`
	UpdateTime string `json:"updateTime"`
}

// AISaveConfig 保存 AI 配置（单例行 id=1；apiKeyEnc 为空表示沿用原密钥）。
func AISaveConfig(ctx context.Context, db *sql.DB, dialect, baseURL, apiKeyEnc, model, updateBy string) error {
	if _, err := db.ExecContext(ctx, dbx.Rebind(dialect, `DELETE FROM ai_config WHERE id=?`), 1); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, dbx.Rebind(dialect,
		`INSERT INTO ai_config (id, base_url, api_key_enc, model, update_by, update_time) VALUES (1,?,?,?,?,?)`),
		baseURL, apiKeyEnc, model, updateBy, time.Now())
	return err
}

// AIGetConfig 读取 AI 配置（无记录返回 nil，不视为错误）。
func AIGetConfig(ctx context.Context, db *sql.DB, dialect string) (*AIConfig, error) {
	var c AIConfig
	var apiKeyEnc sql.NullString
	var ut sql.NullTime
	err := db.QueryRowContext(ctx, dbx.Rebind(dialect,
		`SELECT base_url, api_key_enc, model, update_by, update_time FROM ai_config WHERE id=1`)).
		Scan(&c.BaseURL, &apiKeyEnc, &c.Model, &c.UpdateBy, &ut)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.APIKey = apiKeyEnc.String
	c.UpdateTime = FmtNullTime(ut)
	return &c, nil
}
