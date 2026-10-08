package antiad

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/tg"
)

const chatAdminTTL = 10 * time.Minute

// IsChatAdmin 查此人是否为该群的管理员或群主，结果缓存 10 分钟。
// 查询失败按不是管理员处理：API 故障不得放大权限。
func IsChatAdmin(b *core.Bot, chatID, uid int64) bool {
	key := fmt.Sprintf("%d:%d", chatID, uid)
	if admin, ok := cachesOf(b.Shared).chatAdmin.Get(key); ok {
		return admin
	}

	admin, ok := queryChatAdmin(b, chatID, uid)
	if !ok {
		return false
	}
	cachesOf(b.Shared).chatAdmin.Set(key, admin, chatAdminTTL)
	return admin
}

// queryChatAdmin 向 TG 查一次，ok 为假表示没查成（已记日志，不缓存）。
func queryChatAdmin(b *core.Bot, chatID, uid int64) (admin, ok bool) {
	if uid < 0 {
		// 频道身份：频道当不了群管理员，与之对等的是本群的关联频道——
		// 讨论群里能以它的身份发言的只有群主一方。
		raw, q, err := chatQuery(b, chatID, "getChat", map[string]any{"chat_id": chatID})
		if err != nil {
			slog.Warn("反广告：查询关联频道失败，按普通频道处理",
				"chat", chatID, "sender_chat", uid, "err", err)
			return false, false
		}
		var resp tg.ChatFullResp
		if json.Unmarshal(raw, &resp) != nil || !resp.OK {
			slog.Warn("反广告：查询关联频道返回异常，按普通频道处理",
				"chat", chatID, "sender_chat", uid, "resp", string(raw))
			return false, false
		}
		noteChatQueryBot(b, chatID, q)
		return resp.Result.LinkedChatID == uid, true
	}

	raw, q, err := chatQuery(b, chatID, "getChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
	})
	if err != nil {
		var apiErr *tg.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.AdminRequired():
			// 群里的 bot 一个都不是管理员——这是配置问题，管理员需要知道
			// 把其中某个提为管理员，否则成员身份永远查不到。
			slog.Warn("反广告：群内没有管理员 bot，查不了成员身份，按普通成员处理",
				"chat", chatID, "uid", uid)
		case errors.As(err, &apiErr) && apiErr.NotFound():
			// 查无此人（已离群/从未入群）是 TG 的确定回答，按普通成员处理
			// 即可：结果与查询失败同向（不得放大权限），但不算故障，不记警告，
			// 也不必换 bot 再问一遍。
			if !chatInvisibleToBot(err) {
				// 答得出「查无此人」说明这个 bot 问得动本群成员。
				noteChatQueryBot(b, chatID, q)
			}
		default:
			slog.Warn("反广告：查询群管理员失败，按普通成员处理",
				"chat", chatID, "uid", uid, "err", err)
		}
		return false, false
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		slog.Warn("反广告：查询群管理员返回异常，按普通成员处理",
			"chat", chatID, "uid", uid, "resp", string(raw))
		return false, false
	}
	noteChatQueryBot(b, chatID, q)
	return resp.Result.Status == "administrator" || resp.Result.Status == "creator", true
}

// chatQueryBotTTL 是「哪个 bot 能在这个群查成员」的记录时长。把 bot 免去
// 管理员之后，最多这么久就会重新挑一次。
const chatQueryBotTTL = 30 * time.Minute

// chatQuery 在群内问得动成员状态的 bot 里挑一个，发出 method 调用。
//
// getChatMember 只有群管理员发得动（getChat 也要求是群成员），而发起查询的
// bot 未必是：主 bot 按设计不入群，同群挂着几个 bot 时也只提了其中一个。
// 于是逐个候选试到有一个答得上来，调用方再把成功的那个记下来（见
// noteChatQueryBot）。返回最后一次失败的错误，调用方据此决定怎么记日志。
//
// 网络故障不换 bot 再试：候选走的是同一条出口与同一份代理，换一个只是把
// 传输层超时再等一遍，而这条查询跑在串行的更新处理路径上。
func chatQuery(b *core.Bot, chatID int64, method string,
	payload map[string]any) (json.RawMessage, *core.Bot, error) {

	var last *core.Bot
	var lastErr error
	for _, q := range chatQueryBots(b, chatID) {
		raw, err := q.TG.Call(method, payload)
		if err == nil {
			return raw, q, nil
		}
		var apiErr *tg.APIError
		if !errors.As(err, &apiErr) {
			return nil, q, err
		}
		last, lastErr = q, err
	}
	return nil, last, lastErr
}

// chatQueryBots 排出在该群问得动成员状态的候选 bot，按尝试顺序：上次问成的
// 排最前，其次是发起查询的这个 bot，最后是覆盖本群的其他 bot。
func chatQueryBots(b *core.Bot, chatID int64) []*core.Bot {
	if b.Reg == nil {
		return []*core.Bot{b} // 长轮询只有一个 bot
	}
	var out []*core.Bot
	seen := map[int64]bool{}
	add := func(o *core.Bot) {
		if o == nil || seen[o.BotID()] {
			return
		}
		seen[o.BotID()] = true
		out = append(out, o)
	}
	if id, ok := cachesOf(b.Shared).chatQueryBot.Get(chatID); ok {
		if o, live := b.Reg.LookupID(id); live {
			add(o)
		}
	}
	if !b.IsMainBot() {
		add(b)
	}
	snap := b.Cache.Snap()
	for botID := range snap.Bots {
		// 主 bot 不入群，问它只会得到 chat not found，白跑一趟。
		if rec := snap.Bots[botID]; rec.IsMain {
			continue
		}
		// 挂在该群名下的 bot 才可能是那里的管理员；没配置的 bot 即使
		// 在群里也只是个普通成员，问不动成员状态。
		if _, ok := snap.ChatConf(botID, chatID); !ok {
			continue
		}
		if o, live := b.Reg.LookupID(botID); live {
			add(o)
		}
	}
	return out
}

// noteChatQueryBot 记下这次查成的 bot，供该群后续查询直接用。
func noteChatQueryBot(b *core.Bot, chatID int64, q *core.Bot) {
	if q == nil {
		return
	}
	cachesOf(b.Shared).chatQueryBot.Set(chatID, q.BotID(), chatQueryBotTTL)
}

// chatInvisibleToBot 报告错误是「这个 bot 根本看不到该群」（不是群成员），
// 而不是「此人不在群里」。两者都是 TG 的 400 查无此 X，含义相反：前者换
// 一个 bot 再问还有希望，也绝不能被记成问得动本群的 bot；后者是确定答案。
func chatInvisibleToBot(err error) bool {
	var apiErr *tg.APIError
	return errors.As(err, &apiErr) &&
		strings.Contains(strings.ToLower(apiErr.Desc), "chat not found")
}

// bioTTL 是个人简介的缓存时长。简介本身很少变，但广告号会在被处置后
// 改简介换马甲，所以不做成进程级永久缓存。
const bioTTL = time.Hour

type bioEntry struct {
	bio       string
	firstName string
	lastName  string
	username  string
}

// bioCacheKey 让资料缓存按 (bot, uid) 分开。
//
// 能不能查到一个人取决于**这个 bot 与他有没有共同会话**：工作 bot 入群、
// 判过他的消息，查得到；主 bot 按设计不入群，永远查不到。共用一个键会让
// 主 bot 失败留下的空结果盖掉工作 bot 查得到的结果（反之亦然），所以键里
// 必须带 bot——同一个人的资料，不同 bot 得到的是不同答案。
func bioCacheKey(b *core.Bot, uid int64) string {
	return fmt.Sprintf("%d:%d", b.BotID(), uid)
}

// ForgetUserInfo 丢掉这个 bot 对该用户的资料缓存，让下次 userInfo 重新查。
// 资料刚被改过（头像/简介换马甲）时用。
func ForgetUserInfo(b *core.Bot, uid int64) {
	cachesOf(b.Shared).bio.Delete(bioCacheKey(b, uid))
}

// userInfo 取此人的 getChat 资料（昵称、用户名、简介），带缓存。
//
// 广告号的强特征常常不在消息里而在账号本身：简介写着联系方式、价目、
// 引流话术。这个字段只有 getChat 给得到，而全量送检下不缓存就等于
// 每条群消息多一次 TG 往返，必然撞上速率限制。
//
// 昵称与用户名一并缓存：/check <uid> 这类路径没有消息带这些字段，
// 而冒用国家领导人的硬规则要按资料判人（见 leader.go）。
//
// 查询失败返回空：与群管理员查询同向，TG 故障不得让判定链路停摆。
func userInfo(b *core.Bot, uid int64) bioEntry {
	cache := &cachesOf(b.Shared).bio
	key := bioCacheKey(b, uid)
	if e, ok := cache.Get(key); ok {
		return e
	}

	var empty bioEntry
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": uid})
	if err != nil {
		// 对方从未与 bot 私聊过（或已不在任何共群）时 TG 以 400 chat
		// not found 拒答，这是常态而非故障：按空资料缓存下来，避免
		// 每条消息都重试一次。其余错误才值得警告。
		var apiErr *tg.APIError
		if errors.As(err, &apiErr) && apiErr.NotFound() {
			cache.Set(key, empty, bioTTL)
			return empty
		}
		slog.Warn("反广告：查询账号资料失败", "uid", uid, "err", err)
		return empty
	}
	var resp tg.ChatFullResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		// 对方从未与 bot 私聊过时 TG 也会拒答，这是常态而非故障，
		// 所以按空资料缓存下来，避免每条消息都重试一次。
		cache.Set(key, empty, bioTTL)
		return empty
	}
	e := bioEntry{
		bio: resp.Result.Bio, firstName: resp.Result.FirstName,
		lastName: resp.Result.LastName, username: resp.Result.Username,
	}
	if uid < 0 {
		// 频道身份：资料是频道名与频道简介。
		e.bio = resp.Result.Description
		if e.firstName == "" {
			e.firstName = resp.Result.Title
		}
	}
	cache.Set(key, e, bioTTL)
	return e
}

// userBio 取此人的 Telegram 个人简介（见 userInfo）。
func userBio(b *core.Bot, uid int64) string { return userInfo(b, uid).bio }

// photoTTL 是头像查询结果的缓存时长。头像很少变，但申诉复核必须看到
// 刚补的头像，所以不做永久缓存（申诉路径会先 Delete 再查）。
const photoTTL = 24 * time.Hour

// photoEntry 是缓存里的头像张数。
type photoEntry struct {
	count int
}

// userPhotoCount 取此人的头像张数（getUserProfilePhotos）。
//
// ok 为假表示这次没查成：调用方必须与查到 0 张区分开 —— 把查询
// 失败当无头像，会在 TG 抖动时把正常用户误判为前置号。失败不
// 缓存，下次候选再查。
func userPhotoCount(b *core.Bot, uid int64) (int, bool) {
	if uid <= 0 {
		return 0, true
	}
	cache := &cachesOf(b.Shared).photo
	if e, ok := cache.Get(uid); ok {
		return e.count, true
	}
	raw, err := b.TG.Call("getUserProfilePhotos", map[string]any{
		"user_id": uid, "limit": 1,
	})
	if err != nil {
		slog.Warn("反广告：查询头像失败", "uid", uid, "err", err)
		return 0, false
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			TotalCount int `json:"total_count"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		slog.Warn("反广告：查询头像返回异常", "uid", uid, "resp", string(raw))
		return 0, false
	}
	cache.Set(uid, photoEntry{count: resp.Result.TotalCount}, photoTTL)
	return resp.Result.TotalCount, true
}
