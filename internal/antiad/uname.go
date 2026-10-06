package antiad

import (
	"fmt"
	"log/slog"
	"math"
	"strings"

	"menshen/internal/core"
)

// ---- 用户名随机度与前置号空壳特征组合 ----
//
// 批量注册的广告前置号有一套稳定的「空壳」形态：没头像、没简介、
// 用户名是注册机吐出来的无词形随机串，昵称要么同样是随机串、要么
// 写着业务场景词。这些条件**同时齐备**才判前置号；只命中一部分的
// 不禁言，标记成重点关注（复查间隔封顶 5min），资料补齐后自动解除。
//
// 「有正常个人简介」的用户永远不进这套判定：简介是真人最贵的信号，
// 批量号开局一律不写，写了的按资料广告口径另行判断。

const (
	// unameRandomScore 是「无词形随机串」的评分线（0~100）。线下的是
	// john1990、sarah_smith 这类人取用户名；线上的是注册机吐出的
	// tpiw33abik、vwzbc32xc7、dmfh9r1dgm 这类。
	unameRandomScore = 65
	// unameMinLen / unameMaxLen 是批量注册用户名的典型长度区间（8~16）。
	// 区间内加分，区间外不直接否决：随机度本身与长度无关，长度只是
	// 加重项。
	unameMinLen = 8
	unameMaxLen = 16
	// latinMinScore 是拉丁字母串可评分的最短长度：更短的串（leo、ab）
	// 信息量太少，评分没有意义，字段直接缺席。
	latinMinScore = 5
)

// randEval 是一次随机度评估的结果。
type randEval struct {
	Score int
	// Human 是命中的「人取」特征（词典词、年份、吉利数字等），供
	// reason 引用；为空表示没有发现任何人工痕迹。
	Human []string
}

// unameWords 是内置的常用词表：英文人名、常见名词与汉语拼音姓氏/
// 名字音节（≥4 字母，3 字母的子串误命中率过高，一律不收）。扫描的
// 目的只是把「像词」从「随机」里区分出来，不追求覆盖。
var unameWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(
		// 英文人名（男）
		`james john robert michael william david richard joseph thomas charles
		 chris daniel matthew anthony mark donald steven paul andrew joshua
		 kenneth kevin brian george edward ronald timothy jason jeffrey ryan
		 jacob gary nicholas eric jonathan stephen larry justin scott brandon
		 benjamin samuel gregory alexander patrick frank raymond jack dennis
		 jerry tyler aaron adam henry nathan douglas zachary peter kyle noah
		 ethan jeremy walter christian keith roger terry austin sean gerald
		 carl harold dylan arnold jordan jesse bryan lawrence gabriel bruce
		 logan alan juan wayne ralph randy eugene vincent russell elijah
		 louis bobby philip johnny liam mason owen felix oscar hugo arthur
		 harry leon marcus victor luke simon martin oliver lewis
		 aiden some someone qwerty` +
			// 英文人名（女）
			` mary patricia jennifer linda elizabeth barbara susan jessica sarah
		 karen nancy lisa margaret betty sandra ashley kimberly emily donna
		 michelle carol amanda melissa deborah stephanie rebecca laura sharon
		 cynthia kathleen amy angela shirley anna brenda pamela nicole ruth
		 katherine samantha christine catherine virginia rachel janet emma
		 helen diane julie joyce victoria kelly lauren christina joan evelyn
		 judith olivia frances martha alice jean doris lydia amelia clara
		 lucy maria nina lena maya olga irina elena sofia alina rita zoe
		 iris lily daisy rose hazel ruby pearl summer hannah grace chloe` +
			// 常见名词/昵称用词
			` love king queen prince princess knight wolf fox bear lion tiger
		 dragon eagle hawk raven storm cloud star moon fire snow rain wind
		 water shadow light night dawn devil angel ghost hunter sniper ninja
		 samurai warrior hero legend boss chief captain doctor nurse teacher
		 student hacker coder pixel cyber neon music rock jazz blues punk
		 metal dance disco house techno soccer football tennis golf hockey
		 cricket rugby boxer runner gamer player admin test demo game master
		 lord dark apple lemon mango cherry melon grape peach candy sugar
		 honey coffee tea milk bread rice noodle tofu sushi pizza burger
		 happy lucky smile cloud sunny rainy windy forest river mountain
		 ocean city street home world china paris london berlin panda koala
		 mouse bird fish deer` +
			// 汉语拼音：姓氏与常见名字音节（≥4 字母）
			` wang zhang chen yang huang zhao zhou zheng liang song tang feng
		 deng peng zeng xiao tian dong yuan jiang xiong shao sheng qiu shen
		 cheng guo luo duan fang ping jing qing ying hong zhen yong qiang
		 jian guang chang gang shan chao miao heng juan ling ming hao bin
		 rui kun nan fei yun tao lei jun hui`) {
		unameWords[w] = true
	}
}

// latinOf 取字符串里的 ASCII 字母数字并转小写：用户名与昵称的随机度
// 都只在拉丁部分上有意义，中文/emoji 一律剥掉。
func latinOf(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// randScored 给一段文本打随机度分；拉丁部分太短（<latinMinScore）返回
// nil——字段缺席表示「没有可评分的串」，与「评了 0 分」区分开。
func randScored(s string) *int {
	latin := latinOf(s)
	if len(latin) < latinMinScore {
		return nil
	}
	e := identifierRand(latin)
	return &e.Score
}

// identifierRand 评估一段拉丁字母数字串的随机程度（0~100，越高越像
// 注册机吐出的无词形随机串）。
//
// 评分维度（正值推向「随机」，负值是「人取」的痕迹）：
//   - 长度落在 8~16（批量注册的典型区间）+25，太短只能给低保；
//   - 字母数字混合 +20、数字占比 8%~60% 再 +10（注册机的均衡混合）；
//   - 字符分布的均匀度（归一化香农熵）最多 +20：随机串的字符几乎不重复；
//   - 全辅音（无元音）+10：t p i w 这类连缀拼不出来、多半是生成器；
//   - 含词典词（≥4 字母）每个 -35：john、smith、sarah 一票否决级；
//   - 年份（1990~2099 段）-20、同数字连排（888）-10、连号（123/321）
//     -10、吉利数（520/1314）-10：人挑数字的习惯性证据。
func identifierRand(s string) randEval {
	e := randEval{}
	n := len(s)
	if n == 0 {
		return e
	}

	var letters, digits int
	counts := map[byte]int{}
	for i := 0; i < n; i++ {
		c := s[i]
		counts[c]++
		switch {
		case c >= 'a' && c <= 'z':
			letters++
		case c >= '0' && c <= '9':
			digits++
		}
	}

	switch {
	case n >= unameMinLen && n <= unameMaxLen:
		e.Score += 25
	case n >= latinMinScore:
		e.Score += 8
	}
	if letters > 0 && digits > 0 {
		e.Score += 20
		if r := float64(digits) / float64(n); r >= 0.08 && r <= 0.6 {
			e.Score += 10
		}
	} else if letters > 0 {
		e.Score += 5
	}

	// 归一化香农熵：全串字符各不相同时取满，重复越多越低。
	if n > 1 {
		h := 0.0
		for _, c := range counts {
			p := float64(c) / float64(n)
			h -= p * math.Log2(p)
		}
		e.Score += int(math.Round(20 * h / math.Log2(float64(n))))
	}

	vowels := 0
	for c, k := range counts {
		if strings.ContainsRune("aeiou", rune(c)) {
			vowels += k
		}
	}
	if letters >= 4 {
		switch vr := float64(vowels) / float64(letters); {
		case vowels == 0:
			e.Score += 10
		case vr >= 0.2 && vr <= 0.5:
			e.Score -= 5 // 元音占比像英文单词，倾向人取
		}
	}

	// 词典词扫描：按字母连段（被数字/下划线切断）查子串。
	for _, run := range alphaRuns(s) {
		for w := range unameWords {
			if len(w) >= 4 && strings.Contains(run, w) {
				e.Score -= 35
				e.Human = append(e.Human, "含词形「"+w+"」")
				if len(e.Human) >= 2 {
					break
				}
			}
		}
		if len(e.Human) >= 2 {
			break
		}
	}

	for _, run := range digitRuns(s) {
		switch {
		case len(run) == 4 && isYear(run):
			e.Score -= 20
			e.Human = append(e.Human, "含年份 "+run)
		case hasRepeatDigit(run):
			e.Score -= 10
			e.Human = append(e.Human, "含连排数字 "+run)
		case hasDigitRun(run) || hasLuckyDigits(run):
			e.Score -= 10
			e.Human = append(e.Human, "含规律数字 "+run)
		}
	}
	if strings.ContainsAny(s, "_.") {
		e.Score -= 5 // 分隔符是人取用户名的习惯
	}

	if e.Score < 0 {
		e.Score = 0
	}
	if e.Score > 100 {
		e.Score = 100
	}
	return e
}

// isRandomIdentifier 报告该串是否达到「无词形随机串」评分线。
func isRandomIdentifier(s string) bool {
	return identifierRand(s).Score >= unameRandomScore
}

// alphaRuns / digitRuns 取连续字母段 / 连续数字段。
func alphaRuns(s string) []string {
	return charRuns(s, func(c byte) bool { return c >= 'a' && c <= 'z' })
}
func digitRuns(s string) []string {
	return charRuns(s, func(c byte) bool { return c >= '0' && c <= '9' })
}

func charRuns(s string, keep func(byte) bool) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if keep(s[i]) {
			cur.WriteByte(s[i])
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// isYear 报告四位数字串是否落在人常用的出生年份段（1920~2099，
// 刻意盖住 20xx：2001、2015 这类远多于随机碰撞）。
func isYear(run string) bool {
	if run[0] != '1' && run[0] != '2' {
		return false
	}
	if run[0] == '1' {
		return run[1] == '9'
	}
	return true // 2xxx：2000~2999 全段按年份计
}

// hasRepeatDigit 报告数字段里有没有同一数字连排 3 次以上（888、1000…）。
func hasRepeatDigit(run string) bool {
	for i := 0; i+2 < len(run); i++ {
		if run[i] == run[i+1] && run[i+1] == run[i+2] {
			return true
		}
	}
	return false
}

// hasDigitRun 报告数字段里有没有 3 位以上的连号（123、876…）。
func hasDigitRun(run string) bool {
	for i := 0; i+2 < len(run); i++ {
		d := int(run[i+1]) - int(run[i])
		if (d == 1 || d == -1) && int(run[i+2])-int(run[i+1]) == d {
			return true
		}
	}
	return false
}

// hasLuckyDigits 报告数字段里有没有中文语境的吉利/梗数字。
func hasLuckyDigits(run string) bool {
	for _, lucky := range []string{"520", "1314"} {
		if strings.Contains(run, lucky) {
			return true
		}
	}
	return false
}

// prewarmSceneWords 是昵称里的业务/营销场景词：批量号的昵称常直接
// 写着「XX科技」「专业服务」这类摊位招牌。命中任一即视为「场景词昵称」。
// 只在空壳组合里作为一条必要特征参与，误伤面由其余条件兜住；发现新
// 招牌词直接往表里加。
var prewarmSceneWords = []string{
	"科技", "网络", "传媒", "影视", "文化", "商贸", "电商", "跨境",
	"代购", "直营", "资源", "接单", "招商", "推广", "批发", "厂家",
	"渠道", "代理", "物流", "货运", "换汇", "担保", "秒杀", "引流",
	"网赚", "兼职", "刷单", "代练", "代充", "工作室", "服务商",
	"服务", "合作", "客服", "官方",
	"service", "shop", "store", "official", "agency", "global", "express",
}

// nicknameSceneHit 报告昵称里有没有场景词。
func nicknameSceneHit(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	for _, w := range prewarmSceneWords {
		if strings.Contains(lower, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// prewarmShapeVerdict 是一次前置号空壳特征的核对结果。
//
// 四条必要条件：无头像、无简介、随机用户名、昵称是场景词或随机串。
// **全部满足**才判前置号（Full）；无简介锚点 + 随机用户名/场景昵称
// 任一命中的是重点关注（Watch）；其余不动。用户名长度 8~16 与
// is_premium 只进 Risk 作加重展示，不参与判定。
type prewarmShapeVerdict struct {
	BioEmpty  bool
	UnameRand bool
	NameSus   bool
	NoAvatar  bool // 头像查到且为 0 张；查不到（photo_known=false）不算
	// UnameScore / NameScore 是两处的随机度评分；nil = 没有可评分串。
	UnameScore *int
	NameScore  *int
	// Why 是已满足的必要条件（人话，进流水 reason）；Risk 是加重项。
	Why  []string
	Risk []string
}

// anchor 报告是否进入组合判定：无简介是锚点，配不上随机用户名/场景
// 昵称就不往下走——「有正常个人简介的用户」在这里被整条挡住，永远不会
// 因为用户名随机被关注或处置。
func (v prewarmShapeVerdict) Anchor() bool {
	return v.BioEmpty && (v.UnameRand || v.NameSus)
}

// Full 报告四条必要条件是否全部满足：满足即是前置号，零 AI 处置。
func (v prewarmShapeVerdict) Full() bool {
	return v.BioEmpty && v.UnameRand && v.NameSus && v.NoAvatar
}

// evalPrewarmShape 核对一个账号画像的空壳特征。photos 语义与
// senderProfile.Photos 一致：p.PhotoKnown=false 表示头像数没查到，
// 不得当作无头像（与提示词同口径）。
func evalPrewarmShape(p senderProfile) prewarmShapeVerdict {
	v := prewarmShapeVerdict{
		BioEmpty:   strings.TrimSpace(p.Bio) == "",
		UnameScore: randScored(p.Username),
		NameScore:  randScored(p.FirstName + p.LastName),
		NoAvatar:   p.PhotoKnown && p.Photos != nil && *p.Photos == 0,
	}
	v.UnameRand = v.UnameScore != nil && *v.UnameScore >= unameRandomScore
	v.NameSus = nicknameSceneHit(p.FirstName+p.LastName) ||
		(v.NameScore != nil && *v.NameScore >= unameRandomScore)

	if v.BioEmpty {
		v.Why = append(v.Why, "无个人简介")
	}
	if v.UnameRand {
		v.Why = append(v.Why, fmt.Sprintf("用户名是随机串（%s，随机度 %d）",
			p.Username, *v.UnameScore))
	}
	if nicknameSceneHit(p.FirstName + p.LastName) {
		v.NameSus = true
		v.Why = append(v.Why, "昵称是业务场景词（"+core.TruncateRunes(
			strings.TrimSpace(p.FirstName+p.LastName), 30)+"）")
	} else if v.NameScore != nil && *v.NameScore >= unameRandomScore {
		v.Why = append(v.Why, fmt.Sprintf("昵称是随机串（%s，随机度 %d）",
			strings.TrimSpace(p.FirstName+p.LastName), *v.NameScore))
	}
	if v.NoAvatar {
		v.Why = append(v.Why, "无头像")
	}

	// 加重项：只展示，不参与判定。
	n := len(latinOf(p.Username))
	if n >= unameMinLen && n <= unameMaxLen {
		v.Risk = append(v.Risk, fmt.Sprintf("用户名 %d 位（8~16 典型区间）", n))
	}
	if p.IsPremium {
		v.Risk = append(v.Risk, "开通会员")
	}
	return v
}

// prewarmShapeReason 渲染零 AI 处置与流水用的理由：已满足的必要条件
// 全部列出，加重项单独交代——申诉与管理员复核要看的是证据清单，
// 不是一句「疑似广告」。
func prewarmShapeReason(v prewarmShapeVerdict) string {
	r := "前置号空壳特征齐备：" + strings.Join(v.Why, "、")
	if len(v.Risk) > 0 {
		r += "；加重：" + strings.Join(v.Risk, "、")
	}
	return r
}

// setPrewarmWatch 维护成员的重点关注标记：1 = 复查间隔封顶
// prewarmWatchInterval，资料补齐（组合不再成立）后由复查路径清零。
func setPrewarmWatch(b *core.Bot, chatID, uid int64, watch bool) {
	w := 0
	if watch {
		w = 1
	}
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET prewarm_watch=? WHERE chat_id=? AND user_id=?`, w, chatID, uid); err != nil {
		slog.Error("前置号：维护重点关注标记失败",
			"chat", chatID, "uid", uid, "watch", watch, "err", err)
	}
}
