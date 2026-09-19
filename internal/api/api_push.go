// P1/P2 扩展接口：数据源管理、字典、Excel导出、审计、版本、图表、表单、推送、AI。
package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"litereport/internal/model"
	"litereport/internal/service"
	"litereport/internal/store"
)

// 本文件由 extra.go 按域拆分生成。

// ───── 定时推送 ─────

func (s *Server) handlePushList(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.PushList(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, list)
}

func (s *Server) handlePushSave(w http.ResponseWriter, r *http.Request) {
	var p store.PushRow
	if err := readJSON(r, &p); err != nil || p.ReportCode == "" {
		model.Err(w, "报表编码不能为空")
		return
	}
	ch := strings.ToLower(p.Channel)
	if ch != "" && ch != "email" {
		if p.Webhook == "" {
			model.Err(w, "webhook渠道必须填写机器人地址")
			return
		}
	} else if p.Emails == "" {
		model.Err(w, "报表编码与收件邮箱不能为空")
		return
	}
	if p.IntervalMinutes <= 0 {
		p.IntervalMinutes = 60
	}
	metaDB, dialect := s.mgr.Meta()
	if err := store.PushSave(r.Context(), metaDB, dialect, &p); err != nil {
		model.Err(w, err.Error())
		return
	}
	s.audit(r, "推送任务保存", p.ReportCode, p.Emails)
	model.OK(w, p)
}

func (s *Server) handlePushSend(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	list, err := store.PushList(r.Context(), metaDB, dialect)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	var target *store.PushRow
	for i := range list {
		if list[i].ID == r.PathValue("id") {
			target = &list[i]
		}
	}
	if target == nil {
		model.Err(w, "任务不存在")
		return
	}
	// webhook 渠道直接走机器人
	if ch := strings.ToLower(target.Channel); ch == "dingtalk" || ch == "wecom" {
		head, err := store.HeadFindByKey(r.Context(), metaDB, dialect, target.ReportCode)
		if err != nil {
			model.Err(w, err.Error())
			return
		}
		md := fmt.Sprintf("### LiteReport 定时报表\n- **报表**: %s\n- **时间**: %s\n\n> 手动触发推送测试。", head.ReportName, time.Now().Format("2006-01-02 15:04"))
		if err := service.SendWebhook(r.Context(), ch, target.Webhook, "LiteReport - "+head.ReportName, md); err != nil {
			model.Err(w, "webhook发送失败: "+err.Error())
			return
		}
		s.audit(r, "推送手动发送", target.ReportCode, ch)
		model.OK(w, nil)
		return
	}
	// 手动触发一次（复用调度逻辑，忽略到期检查）
	head, err := store.HeadFindByKey(r.Context(), metaDB, dialect, target.ReportCode)
	if err != nil {
		model.Err(w, err.Error())
		return
	}
	items, _ := store.ItemsByHead(r.Context(), metaDB, dialect, head.ID)
	params, _ := store.ParamsByHead(r.Context(), metaDB, dialect, head.ID)
	// 按报表配置的数据源取数（与定时调度一致），而非元数据库
	targetDB, targetDialect, err := s.mgr.Get(head.DbSource)
	if err != nil {
		model.Err(w, "解析报表数据源失败: "+err.Error())
		return
	}
	data, _, err := service.ExportExcelBytes(r.Context(), targetDB, targetDialect, head, items, params, map[string][]string{})
	if err != nil {
		model.Err(w, s.errOf(err))
		return
	}
	smtpCfg := s.smtpCfg()
	if smtpCfg.Host == "" {
		model.Err(w, "SMTP未配置：请在 config.yaml 的 smtp 段填写后重启")
		return
	}
	if err := service.SendReportMail(service.SmtpConfig(smtpCfg), target.Emails, "LiteReport 定时报表 - "+head.ReportName, "请查收附件报表数据。", head.ReportName+".xlsx", data); err != nil {
		model.Err(w, "发送失败: "+err.Error())
		return
	}
	s.audit(r, "推送手动发送", target.ReportCode, target.Emails)
	model.OK(w, nil)
}

func (s *Server) handlePushDelete(w http.ResponseWriter, r *http.Request) {
	metaDB, dialect := s.mgr.Meta()
	if err := store.PushDelete(r.Context(), metaDB, dialect, r.PathValue("id")); err != nil {
		model.Err(w, err.Error())
		return
	}
	model.OK(w, nil)
}
