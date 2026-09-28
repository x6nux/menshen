package antiad

import (
	"fmt"
	"log/slog"
	"slices"

	"menshen/internal/core"
)

// 申诉单的人工处理入口（Mini App 申诉页）。与自动流程共用状态机、
// 解除路径与通知路径：解除走 liftAppealPenalties，解禁码走
// issueUnlockCode，保证管理员手动操作与 AI 判定的效果一致。

// appealOpen 报告该状态是否未结案（同一人同一 bot 同时只能有一张未结单）。
func appealOpen(status string) bool {
	return slices.Contains(appealOpenStatuses, status)
}

// AdminLiftAppeal 人工通过申诉：撤销全部有效限制并结案。
// 联合封禁是全平台的决定，非主管理员遇到它时拒绝而不是悄悄跳过——
// 静默跳过会让操作者以为全解了，其实全平台封禁还在。
func AdminLiftAppeal(b *core.Bot, appealID, byUID int64, isMain bool) error {
	ap, ok := loadAppealByID(b.Store, appealID)
	if !ok {
		return fmt.Errorf("申诉单不存在")
	}
	if !appealOpen(ap.Status) {
		return fmt.Errorf("该申诉单已结案")
	}
	penalties := effectivePenalties(b, ap.UserID)
	if len(penalties) == 0 {
		updateAppeal(b.Shared, ap.ID,
			`status='lifted', ai_result='skipped', ai_reason='限制已不存在'`)
		return nil
	}
	if !isMain && hasGbanPenalty(penalties) {
		return fmt.Errorf("此人带着联合封禁，只有主管理员能解除")
	}
	liftAppealPenalties(b, ap.ID, ap.UserID, penalties)
	slog.Info("申诉：管理员人工解除", "appeal", ap.ID, "uid", ap.UserID, "by", byUID)
	return nil
}

// AdminRejectAppeal 人工驳回申诉：结案，限制保持原样。
func AdminRejectAppeal(b *core.Bot, appealID, byUID int64) error {
	ap, ok := loadAppealByID(b.Store, appealID)
	if !ok {
		return fmt.Errorf("申诉单不存在")
	}
	if !appealOpen(ap.Status) {
		return fmt.Errorf("该申诉单已结案")
	}
	updateAppeal(b.Shared, ap.ID, `status='rejected'`)
	b.Send(ap.UserID, "你的申诉未通过人工审核，限制维持原样。"+
		"如仍认为有误，请修改账号资料后联系群管理员。", nil)
	slog.Info("申诉：管理员人工驳回", "appeal", ap.ID, "uid", ap.UserID, "by", byUID)
	return nil
}

// AdminIssueCode 给卡在网页验证环节的申诉单人工签发解禁码：
// 网页验证不可用（noweb）或对方一直验不过时的人工出口。
func AdminIssueCode(b *core.Bot, appealID int64) (string, error) {
	ap, ok := loadAppealByID(b.Store, appealID)
	if !ok {
		return "", fmt.Errorf("申诉单不存在")
	}
	if ap.Status != "noweb" && ap.Status != "web" {
		return "", fmt.Errorf("只有等待网页验证的申诉单能签发解禁码")
	}
	code, ok := issueUnlockCode(b.Shared, ap.ID)
	if !ok {
		return "", fmt.Errorf("解禁码生成失败，请重试")
	}
	ap, _ = loadAppealByID(b.Store, ap.ID)
	sendUnlockCode(b, ap.UserID, ap)
	pushAppealCard(b, ap, effectivePenalties(b, ap.UserID),
		appealCardFull, hasGbanPenalty(effectivePenalties(b, ap.UserID)), appealCardExtra{})
	slog.Info("申诉：管理员人工签发解禁码", "appeal", ap.ID)
	return code, nil
}

// AdminRerunAppealAI 重跑 AI 复判：复核出错、或想用最新资料重新评估时用。
// 只对还没结案的单子开放；运行中的复判以 status='ai' 为准，重复点击无害。
func AdminRerunAppealAI(b *core.Bot, appealID int64) error {
	ap, ok := loadAppealByID(b.Store, appealID)
	if !ok {
		return fmt.Errorf("申诉单不存在")
	}
	if ap.Status != "ai" && ap.Status != "web" && ap.Status != "noweb" {
		return fmt.Errorf("该申诉单当前状态不能重跑复核")
	}
	updateAppeal(b.Shared, ap.ID, `status='ai'`)
	startAppealAI(b, ap.ID, ap.UserID)
	return nil
}
