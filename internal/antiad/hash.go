package antiad

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 内容哈希：消息级广告的内容再出现时直接删 ----
//
// 多号轮番刷同一段模板是最常见的刷屏形态，每条都送检既花钱又慢。
// 判成「消息本身就是广告」（scope=message）且要删的，记下内容哈希；
// 同样的内容再出现时不送检、不占送检额度，直接删，禁言交给复判模型——
// 后来者的资料与资历没判过，沿用当初那条的禁言会误伤。
//
// 只记消息级：账号级广告的正文可能只是一句「你好」。按 bot 隔离：一个租户
// 的误判不该删到别人的群里。撤销有两条路：命中后复判判为正常，管理员点「误判」。

// deciderHash 标记「只凭内容哈希、没有复判模型结论」的判定。
const deciderHash = "hash"

// adHashRec 是 ad_hashes 的一行。
type adHashRec struct {
	LogID      int64
	Kind       string
	Confidence float64
}

// adHashKey 是内容的指纹。空白归一：多一个空格、换一行就能绕过的哈希没有意义。
// 先按流水的原文上限截断，误判时才能拿流水里的原文算回同一个哈希。
func adHashKey(text string) string {
	norm := strings.Join(strings.Fields(core.TruncateRunes(text, adTextLimit)), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:16])
}

// rememberAdHash 记下（或刷新）这段内容。
func rememberAdHash(b *core.Bot, text string, v adVerdict, logID int64) {
	now := time.Now().Unix()
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_hashes
		(bot_id,hash,log_id,kind,confidence,hits,created_at,last_hit_at)
		VALUES (?,?,?,?,?,0,?,?)
		ON CONFLICT(bot_id,hash) DO UPDATE SET last_hit_at=excluded.last_hit_at`,
		b.BotID(), adHashKey(text), logID, v.Kind, v.Confidence, now, now); err != nil {
		slog.Error("反广告：记录内容哈希失败", "err", err)
	}
}

// lookupAdHash 查这段内容是否已判为消息级广告。
func lookupAdHash(b *core.Bot, text string) (adHashRec, bool) {
	var r adHashRec
	err := b.Store.Read.QueryRow(`SELECT log_id,kind,confidence FROM ad_hashes
		WHERE bot_id=? AND hash=?`, b.BotID(), adHashKey(text)).
		Scan(&r.LogID, &r.Kind, &r.Confidence)
	return r, err == nil
}

// forgetAdHash 撤掉这段内容的哈希。
func forgetAdHash(b *core.Bot, text string) {
	if _, err := b.Store.Write.Exec(`DELETE FROM ad_hashes WHERE bot_id=? AND hash=?`,
		b.BotID(), adHashKey(text)); err != nil {
		slog.Error("反广告：撤销内容哈希失败", "err", err)
	}
}

// ForgetAdHash 供面板的「误判」使用，text 是流水里的原文。
func ForgetAdHash(b *core.Bot, text string) { forgetAdHash(b, text) }

// hashHit 处理命中哈希的消息，跑在判定 worker 上：先删，再交给复判模型
// 决定罚不罚。没配复判模型、复判失败或复判队列满时只删不罚。
func hashHit(b *core.Bot, snap *store.Snapshot, conf store.BotChat, m *tg.Message,
	profile senderProfile, state adState, h adHashRec, text string) {

	b.Store.Write.Exec(`UPDATE ad_hashes SET hits=hits+1, last_hit_at=?
		WHERE bot_id=? AND hash=?`, time.Now().Unix(), b.BotID(), adHashKey(text))

	note := fmt.Sprintf("（与 #%d 内容相同，直接删除）", h.LogID)
	v := adVerdict{IsAd: true, Confidence: h.Confidence, Kind: h.Kind, Scope: "message",
		Decider: deciderHash, Reason: note}
	pre := adAction{Delete: true}
	preNote := ApplyAction(b, m, pre, conf.Dryrun)

	finish := func(v adVerdict) {
		actOnVerdict(b, snap, conf, m, profile, v, pre, preNote, "")
		if !v.IsAd {
			// 复判说不是广告：这段内容不该再被当成广告直接删。
			forgetAdHash(b, text)
		}
	}
	if !hasLLM(b, snap) {
		finish(v)
		return
	}
	if !b.AdReview(func() {
		enrichSender(b, &state.Sender)
		rv := review(b, snap, state, v, llmSystemPrompt)
		if rv.Decider != deciderHash {
			// 复判成功时理由换成了大模型的，命中哈希这件事也得留在流水里。
			rv.Reason = note + rv.Reason
		}
		finish(rv)
	}) {
		slog.Warn("反广告：复判队列已满，命中哈希只删不罚", "chat", m.Chat.ID, "uid", m.From.ID)
		finish(v)
	}
}
