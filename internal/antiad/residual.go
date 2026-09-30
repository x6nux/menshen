package antiad

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 残留限制复查（后备） ----
//
// 库里「查不到生效限制」不等于 Telegram 侧真的解除了。最典型的是名单
// 扇出的禁言：它有一段时间没有落流水（见 gbanLogAction），撤名单又只
// 解封禁不解禁言 —— 用户看到的是「申诉结案写着限制已不存在，人还是发
// 不了言」。所以他把申诉链接再点一次时，除了告诉他「查不到限制」，
// 还要真去群里核对一遍：库里的记录只是我们自己的账本，权限在 Telegram
// 手里。

const (
	// residualCooldown 是同一个人的复查间隔：连点几下申诉链接不该把
	// 名下所有群一遍遍问过去。
	residualCooldown = 10 * time.Minute
	// residualMaxChats 是一次复查的问询上限，防止极端部署下把申诉入口
	// 拖成全量巡检。
	residualMaxChats = 50
)

// residualSwept 记录每个人上次复查的开始时间。进程内即可：重启后重扫无害。
var residualSwept sync.Map // uid -> time.Time

// residualResult 是一次复查的结论。
type residualResult struct {
	Checked int      // 真问到状态的群数
	Fixed   []string // 发现残留限制并已解除的群
	Pending []string // 还在限时禁言窗口里、等它到期的群
	Skipped []string // 本人另有生效处罚（不是残留）的群
}

// residualSweepAsync 在后台复查该用户在各群的状态并把结果发到他的私聊，
// 返回 false 表示被冷却挡住或判定队列已满（什么都没做）。
func residualSweepAsync(b *core.Bot, dmChat, uid int64) bool {
	if uid <= 0 || b.Shared.Reg == nil {
		return false
	}
	if v, ok := residualSwept.Load(uid); ok {
		if t, ok := v.(time.Time); ok && time.Since(t) < residualCooldown {
			return false
		}
	}
	residualSwept.Store(uid, time.Now())
	return b.AdSubmit(func() {
		b.Send(dmChat, residualSummary(residualSweep(b, uid)), nil)
	})
}

// residualSweep 挨个核对用户在归属人名下各群里的状态，把「库里没有任何
// 记录、Telegram 侧却还被限制」的残留修掉：
//
//	被封禁出群 → unbanChatMember（必须带 only_if_banned，否则等于把
//	            没被封的人踢出去再解封）
//	永久禁言   → 权限全开
//	限时禁言   → 不动：那不是残留，等它到期
//
// 本人另有生效处罚（他自己的广告判定、进群冷判定、仍在名单里）的群跳过，
// 那种限制是该有的。
func residualSweep(b *core.Bot, uid int64) residualResult {
	var out residualResult
	sh := b.Shared
	snap := sh.Cache.Snap()
	owner := b.Owner()

	// 归属人名下所有启用群，同一个群被多个 bot 覆盖只算一次。
	chats := map[int64]store.BotChat{}
	for botID, list := range snap.BotChats {
		rec := snap.Bots[botID]
		if rec == nil || rec.OwnerID != owner {
			continue
		}
		for chatID, c := range list {
			if !c.Enabled {
				continue
			}
			if _, dup := chats[chatID]; !dup {
				chats[chatID] = c
			}
		}
	}
	ids := make([]int64, 0, len(chats))
	for id := range chats {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) > residualMaxChats {
		slog.Info("残留限制复查：群数过多，只查前一部分",
			"uid", uid, "群数", len(ids), "上限", residualMaxChats)
		ids = ids[:residualMaxChats]
	}

	for _, id := range ids {
		name := chatDisplayName(chats[id])
		bots := botsOfChat(sh, id)
		if len(bots) == 0 {
			// bot 都不在群里了：问不到状态，也修不了。
			continue
		}
		skip := false
		for _, bb := range bots {
			if penaltyRowsActive(sh, bb.BotID(), id, uid) {
				skip = true
				break
			}
		}
		if skip {
			out.Skipped = append(out.Skipped, name)
			continue
		}
		st := chatMemberState(bots, id, uid)
		if st == nil {
			continue
		}
		out.Checked++
		if st.Status == "kicked" || st.Status == "restricted" && !st.Member {
			// 已被请出群（restricted + is_member=false 也是被踢的状态）。
			if ok, _ := callFirstOK(bots, "unbanChatMember", map[string]any{
				"chat_id": id, "user_id": uid, "only_if_banned": true}); ok {
				slog.Info("残留限制复查：已解封", "chat", id, "uid", uid)
				out.Fixed = append(out.Fixed, name)
			}
			continue
		}
		if !st.Muted {
			continue
		}
		if st.Until > time.Now().Unix() {
			out.Pending = append(out.Pending,
				name+"（到 "+formatTS(sh, st.Until)+"）")
			continue
		}
		for _, bb := range bots {
			if ok, _ := Unmute(bb, id, uid); !ok {
				continue
			}
			slog.Info("残留限制复查：已解除禁言", "chat", id, "uid", uid)
			out.Fixed = append(out.Fixed, name)
			break
		}
	}
	slog.Info("残留限制复查完成", "uid", uid, "核对群数", out.Checked,
		"已修好", len(out.Fixed), "待到期", len(out.Pending), "跳过", len(out.Skipped))
	return out
}

// memberState 是复查关心的成员状态。
type memberState struct {
	Status string
	Member bool // 是否还在群里（restricted 才有意义）
	Muted  bool // can_send_messages=false，即被禁言
	Until  int64
}

// chatMemberState 用群里任一 bot 问一次成员状态；全失败返回 nil。
func chatMemberState(bots []*core.Bot, chatID, uid int64) *memberState {
	for _, b := range bots {
		raw, err := b.TG.Call("getChatMember", map[string]any{
			"chat_id": chatID, "user_id": uid,
		})
		if err != nil {
			continue
		}
		var resp tg.ChatMemberResp
		if json.Unmarshal(raw, &resp) != nil || !resp.OK {
			continue
		}
		st := &memberState{Status: resp.Result.Status, Member: true,
			Until: resp.Result.UntilDate}
		if resp.Result.CanSendMessages != nil && !*resp.Result.CanSendMessages {
			st.Muted = true
		}
		return st
	}
	return nil
}

// chatDisplayName 给群配一个能在私聊里念出来的名字。
func chatDisplayName(c store.BotChat) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return strconv.FormatInt(c.ChatID, 10)
}

// residualSummary 把复查结果渲染成用户能看懂的一段话（HTML 转义群名）。
func residualSummary(r residualResult) string {
	var sb strings.Builder
	sb.WriteString("🔎 <b>复查完成</b>\n")
	if len(r.Fixed) > 0 {
		sb.WriteString("\n发现残留的限制，已经解除：\n")
		for _, n := range r.Fixed {
			sb.WriteString("• " + html.EscapeString(n) + " ✅\n")
		}
		sb.WriteString("\n现在应该能正常发言了。若某个群还是发不出，把群名发给我。")
	} else {
		fmt.Fprintf(&sb, "\n核对了 %d 个群，没有发现残留的限制。", r.Checked)
		if len(r.Pending) == 0 && len(r.Skipped) == 0 {
			sb.WriteString("\n若某个群发不了言，把群名发给我，或联系群管理员。")
		}
	}
	if len(r.Pending) > 0 {
		sb.WriteString("\n\n这些群里你还有<b>限时禁言</b>，到期自动解除：\n")
		for _, n := range r.Pending {
			sb.WriteString("• " + html.EscapeString(n) + "\n")
		}
	}
	if len(r.Skipped) > 0 {
		sb.WriteString("\n这些群里你另有生效中的处罚，不在本次复查范围：\n")
		for _, n := range r.Skipped {
			sb.WriteString("• " + html.EscapeString(n) + "\n")
		}
	}
	return strings.TrimSpace(sb.String())
}
