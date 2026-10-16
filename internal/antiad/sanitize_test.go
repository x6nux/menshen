package antiad

import (
	"strings"
	"testing"

	"menshen/internal/core"
)

// 线上实际用到的拆词字符：TAG 区（U+E00xx）与变体选择符补充区（U+E01xx），
// 外加常见的零宽空格与变体选择符基本区。
var obfChars = []rune{
	0xE0064, 0xE0137, 0xE0140, 0xE0143, 0xE016A, 0xE0172,
	0xE0192, 0xE01D8, 0xE01E0, 0xE0026, 0xE0035, 0xE004C, 0xE005D,
	0x200B, 0x2060, 0xFE0F, 0x00AD,
}

func tryWrapObf(s string) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		b.WriteRune(r)
		if i < len(obfChars) {
			b.WriteRune(obfChars[i])
		}
	}
	return b.String()
}

func TestSanitizeVisibleStripsObfuscation(t *testing.T) {
	want := "免费高速VPN 看简介 5"
	got := sanitizeVisible(tryWrapObf(want))
	if got != want {
		t.Fatalf("sanitizeVisible = %q, want %q", got, want)
	}
	// 普通文本与空白不受影响：不能把正常中文、字母、空格或 emoji 一起剥掉。
	plain := "正常消息 hello world 🚀"
	if sanitizeVisible(plain) != plain {
		t.Fatalf("sanitizeVisible 改动了正常文本: %q", sanitizeVisible(plain))
	}
}

func TestAdStateSanitized(t *testing.T) {
	obfText := tryWrapObf("免费高速VPN 看简介 5")
	obfBio := tryWrapObf("VPN链接 https://linkoso.shop/x")
	st := adState{
		Chat:    adChatInfo{Title: tryWrapObf("技术群")},
		Message: adMessageInfo{Text: obfText},
		Sender: senderProfile{
			FirstName: tryWrapObf("免费高速VPN"),
			Bio:       obfBio,
			BioLinks:  []linkInfo{{Handle: tryWrapObf("@x_bot"), Title: tryWrapObf("频道")}},
		},
		RecentContext: []core.CtxMsg{{Name: tryWrapObf("某人"), Text: tryWrapObf("上一条")}},
	}
	got := st.sanitized()
	if got.Message.Text != "免费高速VPN 看简介 5" {
		t.Fatalf("正文未净化: %q", got.Message.Text)
	}
	if got.Sender.Bio != "VPN链接 https://linkoso.shop/x" || got.Sender.FirstName != "免费高速VPN" {
		t.Fatalf("资料未净化: %+v", got.Sender)
	}
	if got.Sender.BioLinks[0].Handle != "@x_bot" || got.Sender.BioLinks[0].Title != "频道" {
		t.Fatalf("bio_links 未净化: %+v", got.Sender.BioLinks)
	}
	if got.RecentContext[0].Text != "上一条" || got.RecentContext[0].Name != "某人" {
		t.Fatalf("上下文未净化: %+v", got.RecentContext)
	}
	if got.Chat.Title != "技术群" {
		t.Fatalf("群标题未净化: %q", got.Chat.Title)
	}
}

// 归一化后，模型摘录的干净证据应当能通过一致性门 —— 这正是线上那条被
// 「简简」抄错打回的反例在净化后的状态。
func TestEvidenceGateAfterSanitize(t *testing.T) {
	st := adState{
		Message: adMessageInfo{Text: sanitizeVisible(tryWrapObf("免费高速VPN 看简介 5"))},
		Sender: senderProfile{
			FirstName: "免费高速VPN",
			Bio:       "VPN链接 https://linkoso.shop/channel/x",
		},
	}
	v := adVerdict{IsAd: true, Kind: "promo", Evidence: []string{
		"免费高速VPN", "免费高速VPN 看简介 5", "https://linkoso.shop/channel/x",
	}}
	if m := evidenceMisses(st, v); len(m) != 0 {
		t.Fatalf("净化后证据仍对不上: %v", m)
	}
}

// 不可见字符若原样留在语料里，逐字摘录的门就应该对不上 —— 说明净化不是
// 可选项（旧实现漏了 U+E00xx/U+E01xx，模型抄写时极易错位）。
func TestEvidenceGateRejectsObfuscatedGGate(t *testing.T) {
	st := adState{Message: adMessageInfo{Text: tryWrapObf("免费高速VPN 看简介 5")}}
	v := adVerdict{IsAd: true, Evidence: []string{"免费高速VPN 看简介 5"}}
	// stripInvisible 已覆盖这些字符，故这里应通过：核对的语料与证据都被
	// 归一化。这个断言防止未来有人把 stripInvisible 的范围收窄回去。
	if m := evidenceMisses(st, v); len(m) != 0 {
		t.Fatalf("证据门的归一化失效: %v", m)
	}
}
