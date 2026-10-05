package antiad

// join_mutes.kind 的取值：profile = 资料里有广告（冷判定/延迟复查），
// prewarm = 前置号识别（空壳+招呼的综合特征）。申诉提示词按它分流。
const (
	kindProfile = "profile"
	kindPrewarm = "prewarm"

	actionJoinMuted      = "join_muted"
	actionPrewarmMuted   = "prewarm_muted"
	actionPrewarmChecked = "prewarm_checked"
)
