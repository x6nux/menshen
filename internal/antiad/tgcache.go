package antiad

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"menshen/internal/core"
	"menshen/internal/tg"
)

const chatAdminTTL = 10 * time.Minute

// isChatAdmin 查此人是否为该群的管理员或群主，结果缓存 10 分钟。
// 查询失败按「不是管理员」处理：API 故障不得放大权限。
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
		raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
		var resp tg.ChatFullResp
		if err != nil || json.Unmarshal(raw, &resp) != nil || !resp.OK {
			slog.Warn("反广告：查询关联频道失败，按普通频道处理",
				"chat", chatID, "sender_chat", uid, "err", err)
			return false, false
		}
		return resp.Result.LinkedChatID == uid, true
	}

	raw, err := b.TG.Call("getChatMember", map[string]any{
		"chat_id": chatID, "user_id": uid,
	})
	if err != nil {
		// 查无此人（已离群/从未入群）是 TG 的确定回答，按普通成员处理
		// 即可：结果与查询失败同向（不得放大权限），但不算故障，别刷警告。
		var apiErr *tg.APIError
		if errors.As(err, &apiErr) && apiErr.NotFound() {
			return false, false
		}
		slog.Warn("反广告：查询群管理员失败，按普通成员处理",
			"chat", chatID, "uid", uid, "err", err)
		return false, false
	}
	var resp tg.ChatMemberResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		slog.Warn("反广告：查询群管理员返回异常，按普通成员处理",
			"chat", chatID, "uid", uid, "resp", string(raw))
		return false, false
	}
	return resp.Result.Status == "administrator" || resp.Result.Status == "creator", true
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

// userInfo 取此人的 getChat 资料（昵称、用户名、简介），带缓存。
//
// 广告号的强特征常常不在消息里而在账号本身：简介写着联系方式、价目、
// 引流话术。这个字段只有 getChat 给得到，而全量送检下不缓存就等于
// 每条群消息多一次 TG 往返，必然撞上速率限制。
//
// 昵称与用户名一并缓存：/check <uid> 这类路径没有消息带这些字段，
// 而「冒用国家领导人」的硬规则要按资料判人（见 leader.go）。
//
// 查询失败返回空：与群管理员查询同向，TG 故障不得让判定链路停摆。
func userInfo(b *core.Bot, uid int64) bioEntry {
	cache := &cachesOf(b.Shared).bio
	if e, ok := cache.Get(uid); ok {
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
			cache.Set(uid, empty, bioTTL)
			return empty
		}
		slog.Warn("反广告：查询账号资料失败", "uid", uid, "err", err)
		return empty
	}
	var resp tg.ChatFullResp
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		// 对方从未与 bot 私聊过时 TG 也会拒答，这是常态而非故障，
		// 所以按空资料缓存下来，避免每条消息都重试一次。
		cache.Set(uid, empty, bioTTL)
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
	cache.Set(uid, e, bioTTL)
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
// ok 为假表示这次没查成：调用方必须与「查到 0 张」区分开 —— 把查询
// 失败当无头像，会在 TG 抖动时给正常用户扣一顶前置号的帽子。失败不
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
