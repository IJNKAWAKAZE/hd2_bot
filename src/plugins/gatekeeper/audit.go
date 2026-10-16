// audit.go 是入群审计：每个新成员的归宿都留一条记录（谁、从哪条路来、结果、为什么）。
//
// 为什么必须记：验证出错时（误踢、漏放、机器人没权限）群里看到的现象都是「这个人怎么没了」，
// 只有审计能回答是哪一步的动作把它弄没的。可选再发一份到审计频道，让管理员不必翻服务器日志。
package gatekeeper

import (
	"fmt"

	"hd2_bot/src/core/bot"
)

// 审计结果取值。前四个是"人是怎么进/出这个群的"，后四个是验证本身的结算结果。
const (
	auditJoined    = "自行进群"   // 自己点链接进群，进入群内验证
	auditRequested = "提交入群申请" // 申请模式，进入私聊验证
	auditInvited   = "邀请入群"   // 被其它成员邀请，按配置免验证
	auditSkipped   = "未验证放行"  // 出题或发卡失败时的兜底放行
	auditPassed    = "验证通过"
	auditKicked    = "踢出"
	auditBanned    = "封禁"
	auditDeclined  = "拒绝申请"
)

// audit 记一条入群审计：本地日志 + （配置了的话）审计频道。
func (h *Handler) audit(chatID, userID int64, name, result, reason string) {
	h.log("[gatekeeper] 入群审计 chat=%d user=%d name=%q 结果=%s 说明=%s", chatID, userID, name, result, reason)
	if h.cfg.AuditChannel == 0 {
		return
	}
	text := fmt.Sprintf("入群审计\n成员：%s（`%d`）\n群：`%d`\n结果：%s\n说明：%s",
		bot.Escape(name), userID, chatID, bot.Escape(result), bot.Escape(reason))
	if err := h.telegram.SendText(h.cfg.AuditChannel, text); err != nil {
		h.log("[gatekeeper] 审计频道发送失败 chat=%d err=%v", h.cfg.AuditChannel, err)
	}
}
