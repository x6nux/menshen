package antiad

import (
	"strings"
	"testing"
)

// 随机度算法：注册机生成的无词形随机串应达到阈值，人工取的常规用户名
// 应低于阈值。
func TestIdentifierRand(t *testing.T) {
	random := []string{
		// 提示词中的三个样本
		"tpiw33abik", "vwzbc32xc7", "dmfh9r1dgm",
		"xk7dp2mqn9", "ahmx88kd3", "q7zt4vxr2m",
	}
	for _, s := range random {
		e := identifierRand(s)
		if e.Score < unameRandomScore {
			t.Errorf("%s: score = %d，应 ≥ %d（判为随机串）", s, e.Score, unameRandomScore)
		}
		if len(e.Human) > 0 {
			t.Errorf("%s: 不应命中人取特征，得到 %v", s, e.Human)
		}
	}

	human := []string{
		"someone", "john1990", "sarah_smith", "David_88", "mike12345",
		"user888", "ahmed999", "aiden8827", "xiaoming1990", "wangli2024",
		"blue_tiger", "qwerty12345", "lisa2024", "happy888", "telegram2024",
	}
	for _, s := range human {
		e := identifierRand(latinOf(s))
		if e.Score >= unameRandomScore {
			t.Errorf("%s: score = %d，应 < %d（人取用户名不该过线），特征 %v",
				s, e.Score, unameRandomScore, e.Human)
		}
		if len(e.Human) == 0 {
			t.Errorf("%s: 应命中至少一项人取特征", s)
		}
	}
}

// randScored 的缺席语义：中文昵称与过短的串不评分（nil），不得当 0 分。
func TestRandScored(t *testing.T) {
	if got := randScored("小明"); got != nil {
		t.Errorf("中文昵称应不评分，得到 %v", *got)
	}
	if got := randScored("Leo"); got != nil {
		t.Errorf("过短的拉丁串应不评分，得到 %v", *got)
	}
	if got := randScored("tpiw33abik"); got == nil || *got < unameRandomScore {
		t.Errorf("随机串应评分且过线，得到 %v", got)
	}
	if got := randScored("john1990"); got == nil || *got >= unameRandomScore {
		t.Errorf("人取用户名应评分且不过线，得到 %v", got)
	}
}

// 空壳特征组合：四条必要条件全齐才 Full；无简介是锚点；有正常简介的
// 用户无论其余条件多像都不进组合（不误伤）。
func TestPrewarmShapeVerdict(t *testing.T) {
	zero, two := 0, 2
	full := senderProfile{
		Username: "tpiw33abik", FirstName: "在线服务",
		PhotoKnown: true, Photos: &zero,
	}
	v := evalPrewarmShape(full)
	if !v.Full() || v.Anchor() == false || !v.NoAvatar || !v.BioEmpty ||
		!v.UnameRand || !v.NameSus {
		t.Fatalf("全条件齐备应 Full： %+v", v)
	}
	if len(v.Why) != 4 {
		t.Errorf("Why 应列出全部四条，得到 %v", v.Why)
	}

	// 有正常个人简介：绝不进入组合（用户红线）。
	withBio := full
	withBio.Bio = "热爱生活，喜欢旅行"
	if v = evalPrewarmShape(withBio); v.Anchor() || v.Full() {
		t.Errorf("有正常简介的用户不该进组合：anchor=%v full=%v", v.Anchor(), v.Full())
	}

	// 头像数没查到：不得当作无头像，只到关注档。
	unknown := full
	unknown.PhotoKnown = false
	if v = evalPrewarmShape(unknown); v.Full() || !v.Anchor() {
		t.Errorf("头像未知应只到关注档：full=%v anchor=%v", v.Full(), v.Anchor())
	}

	// 有头像：到不了处置档，仍是关注档。
	withPhoto := full
	withPhoto.Photos = &two
	if v = evalPrewarmShape(withPhoto); v.Full() || !v.Anchor() {
		t.Errorf("有头像应只到关注档：full=%v anchor=%v", v.Full(), v.Anchor())
	}

	// 只有随机用户名：关注。
	unameOnly := senderProfile{Username: "tpiw33abik"}
	if v = evalPrewarmShape(unameOnly); !v.Anchor() || v.Full() {
		t.Errorf("随机用户名+无简介应关注：anchor=%v full=%v", v.Anchor(), v.Full())
	}

	// 只有场景词昵称：关注。
	nameOnly := senderProfile{FirstName: "专业服务"}
	if v = evalPrewarmShape(nameOnly); !v.Anchor() || v.Full() {
		t.Errorf("场景词昵称+无简介应关注：anchor=%v full=%v", v.Anchor(), v.Full())
	}

	// 只有随机昵称：关注。
	randName := senderProfile{FirstName: "gp7zk2xwq9"}
	if v = evalPrewarmShape(randName); !v.Anchor() || v.Full() {
		t.Errorf("随机昵称+无简介应关注：anchor=%v full=%v", v.Anchor(), v.Full())
	}

	// 只有空简介，其余都正常：不关注。
	emptyOnly := senderProfile{Username: "someone", FirstName: "小明"}
	if v = evalPrewarmShape(emptyOnly); v.Anchor() {
		t.Errorf("仅无简介不该关注： %+v", v)
	}

	// 加重项：长度 8~16 与会员只进 Risk，不改变判定。
	risk := full
	risk.IsPremium = true
	v = evalPrewarmShape(risk)
	joined := strings.Join(v.Risk, "、")
	if !strings.Contains(joined, "8~16") || !strings.Contains(joined, "会员") {
		t.Errorf("加重项应含长度与会员，得到 %v", v.Risk)
	}

	// 理由要能看：证据清单 + 加重项。
	if r := prewarmShapeReason(v); !strings.Contains(r, "无头像") ||
		!strings.Contains(r, "随机") || !strings.Contains(r, "会员") {
		t.Errorf("reason 应列出证据与加重项，得到 %q", r)
	}
}

// 场景词昵称表：中英文招牌词命中，普通名字不命中。
func TestNicknameSceneHit(t *testing.T) {
	for _, s := range []string{"企鹅科技", "专业服务", "全球代购", "XX工作室",
		"Fast Shop", "Official Store"} {
		if !nicknameSceneHit(s) {
			t.Errorf("%q 应命中场景词", s)
		}
	}
	for _, s := range []string{"小明", "张伟", "Aiden", "热爱生活", "Lee"} {
		if nicknameSceneHit(s) {
			t.Errorf("%q 不应命中场景词", s)
		}
	}
}
