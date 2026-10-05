package antiad

import (
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// join_mutes.kind 的取值：profile = 资料里有广告（冷判定/延迟复查），
// prewarm = 前置号识别（空壳+招呼的综合特征）。申诉提示词按它分流。
const (
	kindProfile = "profile"
	kindPrewarm = "prewarm"

	actionJoinMuted      = "join_muted"
	actionPrewarmMuted   = "prewarm_muted"
	actionPrewarmChecked = "prewarm_checked"
)

const (
	// prewarmJoinWindow 是首条消息复核的进群时间窗：只有刚进群的人算
	// 新成员，超窗不给复核（可能是老成员的第一条留底）。
	prewarmJoinWindow = 72 * time.Hour
	// prewarmTextMax 是候选消息的长度上限（字符）。短到不足以承载一条
	// 正常的技术讨论，才值得为它花一次账号复核。
	prewarmTextMax = 8
)

// prewarmTrimCut 是候选消息归一化时剥掉的空白与常见标点。
const prewarmTrimCut = " \t\r\n！!。.，,？?~～…·、:：;；“”\"'‘’()（）[]【】"

// prewarmCandidate 报告这条消息是否触发首条消息账号复核。
//
// 它只圈候选、不判定：白名单/豁免/联封/必封规则/内容哈希都已在前面的
// 分支处理过；这里命中只表示「值得花一次 AI 看看这个账号」。
func prewarmCandidate(b *core.Bot, snap *store.Snapshot, gm groupMember,
	m *tg.Message, edited bool) bool {

	if m == nil || m.From == nil || edited {
		return false
	}
	if snap.BotSettingInt(b.BotID(), "antiad_prewarm", 0) != 1 {
		return false
	}
	if gm.MsgCount != 1 || gm.JoinedAt <= 0 {
		return false
	}
	if time.Since(time.Unix(gm.JoinedAt, 0)) > prewarmJoinWindow {
		return false
	}
	text := strings.TrimSpace(msgText(m))
	if text == "" || len([]rune(strings.Trim(text, prewarmTrimCut))) > prewarmTextMax {
		return false
	}
	for _, e := range m.Entities {
		switch e.Type {
		case "url", "text_link", "mention", "text_mention":
			return false
		}
	}
	for _, e := range m.CaptionEntities {
		switch e.Type {
		case "url", "text_link", "mention", "text_mention":
			return false
		}
	}
	return true
}
