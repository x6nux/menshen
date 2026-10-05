package antiad

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 资料形状哈希（同模板批量账号跨账号复用，§10.3） ----
//
// 批量号常常同模板：昵称、简介是同一套文案，只换邀请链接、号码或名字。
// 第一个账号被判广告后把资料的「形状」学下来，后面的同形状账号零 AI
// 直接禁言，模板的变化不会白白再烧一遍判定。

// 形状里的占位符：链接/号码/@用户名。用私用区单字符中转，最后再换成
// 可读的 [链接]/[数]/[号]，避免在归一化中间步骤里被标点清理误删。
const (
	shapeLinkMark  = '\ue000'
	shapeAtMark    = '\ue001'
	shapeDigitMark = '\ue002'
)

var (
	// shapeLinkRe 覆盖资料里常见的链接形态（私聊群邀请、短链、自建站）。
	shapeLinkRe = regexp.MustCompile(`(?:https?://|t\.me/|www\.)\S+`)
	// shapeAtRe 只替换有实义的 @用户名（≥3 位）；单独一个 @ 当标点丢掉。
	shapeAtRe = regexp.MustCompile(`@[A-Za-z0-9_]{3,}`)
	// shapeDigitRe 把任何数字串压成一个占位符：同模板的价目/数量不同
	// 不该影响形状。
	shapeDigitRe = regexp.MustCompile(`[0-9]+`)
)

// shapeStrip 去掉零宽字符与不可见格式符：攻击者用它们拆词规避匹配，
// 残留会让同一模板产生多个形状。
var shapeStrip = strings.NewReplacer(
	"\u00ad", "", // soft hyphen
	"\u200b", "", "\u200c", "", "\u200d", "", "\u200e", "", "\u200f", "",
	"\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
	"\u2060", "", "\u2061", "", "\u2062", "", "\u2063", "", "\u2064", "",
	"\u3164", "", // hangul filler
	"\ufeff", "", // BOM
)

// profileShape 把资料归一化成「形状」：小写、去零宽与标点、压缩空白，
// 链接→[链接]、@用户名→[号]、数字段→[数]。
//
// 只取 bio：昵称是低熵文本（Robert Williamson/John Smith 这类同名者众），
// 拿它做形状会显著扩大零 AI 误伤面；号商模板都在 bio。bio 为空 → 空串，
// 不参与复用。有效字母不足 4 个或只剩占位符同样返回空串（没有区分度）。
func profileShape(p senderProfile) string {
	src := strings.TrimSpace(p.Bio)
	if src == "" {
		return ""
	}

	s := shapeStrip.Replace(src)
	s = strings.ToLower(s)
	s = shapeLinkRe.ReplaceAllString(s, string(shapeLinkMark))
	s = shapeAtRe.ReplaceAllString(s, string(shapeAtMark))
	s = shapeDigitRe.ReplaceAllString(s, string(shapeDigitMark))

	var b strings.Builder
	letters, lastSpace := 0, false
	for _, r := range s {
		switch {
		case r == shapeLinkMark || r == shapeAtMark || r == shapeDigitMark:
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsLetter(r):
			b.WriteRune(r)
			letters++
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			// 标点、emoji 与其他符号：丢掉
		}
	}
	if letters < 4 {
		return ""
	}
	out := strings.TrimSpace(b.String())
	// 占位符两侧的空白并入占位符：「洗资 https://…」与「洗资https://…」
	// 是同一模板，不该因为一个空格分裂成两个形状。
	for _, m := range []string{string(shapeLinkMark), string(shapeAtMark),
		string(shapeDigitMark)} {
		out = strings.ReplaceAll(out, " "+m, m)
		out = strings.ReplaceAll(out, m+" ", m)
	}
	out = strings.ReplaceAll(out, string(shapeLinkMark), "[链接]")
	out = strings.ReplaceAll(out, string(shapeAtMark), "[号]")
	out = strings.ReplaceAll(out, string(shapeDigitMark), "[数]")
	return out
}

// learnableShape 返回一条**值得学习**的资料形状：只有 bio 自身呈现了可疑
// 特征（链接、联系方式、招揽话术）时才返回非空。
//
// 为什么加这道门：profileShape 只看 bio，但一条进群限制可能并非因 bio 而
// 成立 —— 前置号看的是头像与随机用户名，资料必封规则可能命中昵称，AI 也
// 可能因用户名判广告。此时把那段普通简介（「热爱生活，喜欢旅行」）学成
// 模板，零 AI 的 shapeMute 就会误伤用同一句简介的正常人，而形状表是全
// 平台共享的。宁可少学（漏掉检测），不可误学（误伤面被放大）。
func learnableShape(p senderProfile) string {
	bio := strings.ToLower(p.Bio)
	if strings.TrimSpace(bio) == "" {
		return ""
	}
	for _, h := range coldPrefilterHints {
		if h.hit(bio) {
			return profileShape(p)
		}
	}
	return ""
}

// profileShapeRec 是 profile_shapes 的一行。
type profileShapeRec struct {
	Shape     string
	Kind      string
	Hits      int64
	LastHit   int64
	Sample    string
	CreatedAt int64
}

// lookupProfileShape 查一个资料形状是否已经学习过。
func lookupProfileShape(s *store.Store, shape string) (profileShapeRec, bool) {
	var r profileShapeRec
	err := s.Read.QueryRow(`SELECT shape,kind,hits,last_hit,sample,created_at
		FROM profile_shapes WHERE shape=?`, shape).
		Scan(&r.Shape, &r.Kind, &r.Hits, &r.LastHit, &r.Sample, &r.CreatedAt)
	if err != nil {
		return r, false
	}
	return r, true
}

// learnProfileShape 记下一条资料形状：首次 insert，之后 hits+1 并刷新
// 样本、分类与 last_hit（样本取最近一次，跟得上模板演化）。
func learnProfileShape(s *store.Store, shape, kind, sample string, now int64) {
	if shape == "" {
		return
	}
	if _, err := s.Write.Exec(`INSERT INTO profile_shapes
		(shape,kind,hits,last_hit,sample,created_at) VALUES (?,?,1,?,?,?)
		ON CONFLICT(shape) DO UPDATE SET
		  kind=excluded.kind, hits=hits+1, last_hit=excluded.last_hit,
		  sample=excluded.sample`,
		shape, kind, now, core.TruncateRunes(sample, 200), now); err != nil {
		slog.Warn("反广告：资料形状落库失败", "shape", shape, "err", err)
	}
}

// removeProfileShape 删除一条形状（限制被撤销/申诉解除时用）：形状只从
// 已成立的处罚学习，处罚被推翻后它不该继续复用。
func removeProfileShape(s *store.Store, shape string) {
	if shape == "" {
		return
	}
	if _, err := s.Write.Exec(`DELETE FROM profile_shapes WHERE shape=?`, shape); err != nil {
		slog.Warn("反广告：删除资料形状失败", "shape", shape, "err", err)
	}
}

// shapeMute 在资料形状命中已学习模板时直接禁言（零 AI）。演练群只落
// dryrun 流水（不刷新形状热度）；quiet=true 用于批量探测（不发群通知）。
// 返回 (是否命中, 是否已落入 join_mutes)。
//
// Shape 随处罚落进 join_mutes.shape：撤销/申诉解除这条限制时按它反查
// 删除模板（同 AI/enforce 路径）。命中计数不在这里手动 bump——交给
// applyJoinMuteNotify 成功后的 learn 计一次，避免双计；演练不落库也就
// 不会让错误形状靠 dryrun 命中续命。
func shapeMute(b *core.Bot, conf store.BotChat, u *tg.TGUser, p senderProfile,
	body, note string, quiet bool) (bool, bool) {
	shape := profileShape(p)
	if shape == "" {
		return false, false
	}
	rec, ok := lookupProfileShape(b.Store, shape)
	if !ok {
		return false, false
	}
	v := adVerdict{IsAd: true, Confidence: 1, Kind: rec.Kind, Scope: "account",
		Decider: "phash",
		Reason: fmt.Sprintf("资料形态与已判定广告号一致（样本：%s，已命中 %d 次）",
			core.TruncateRunes(rec.Sample, 60), rec.Hits+1)}
	if conf.Dryrun {
		logAd(b, &tg.Message{Chat: &tg.Chat{ID: conf.ChatID, Title: conf.Title},
			From: u, Text: body}, v, "dryrun:join_muted", note+"（演练）")
		return true, false
	}
	applyJoinMuteNotify(b, conf, u, v, joinMuteSpec{
		Kind: kindProfile, Action: actionJoinMuted, Note: note,
		Body: body, Reason: v.Reason, Announce: conf.GroupAlert,
		Shape: shape,
		Quiet: quiet,
	})
	_, muted := loadJoinMute(b.Store, conf.ChatID, u.ID)
	return true, muted
}
