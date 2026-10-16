package core

import (
	"html"
	"slices"
	"strconv"
	"strings"

	"menshen/internal/store"
)

// ChatRef 是一个群用于展示的引用：群名 + 公开 @ 名。
//
// 群 ID 对人没有意义，凡是把某个群呈现给人的地方（告警、流水、状态回执、
// 申诉页、面板）都用它渲染：公开群渲染成带 t.me 链接的群名，私有群或
// 名字未知时退回群名、再退回数字 id。
type ChatRef struct {
	ID       int64
	Title    string
	Username string
}

// LookupChatRef 在快照里全平台查这个 chat_id 的展示引用。同一个群可能挂在
// 多个 bot 名下（配置各存一份），标题与 @ 名各取 bot_id 最小的那个非空值
// —— map 遍历无序，定序才能保证同样的状态渲染出同样的引用。
func LookupChatRef(snap *store.Snapshot, chatID int64) ChatRef {
	r := ChatRef{ID: chatID}
	if snap == nil {
		return r
	}
	ids := make([]int64, 0, len(snap.Bots))
	for id := range snap.Bots {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c, ok := snap.ChatConf(id, chatID)
		if !ok {
			continue
		}
		if r.Title == "" {
			r.Title = strings.TrimSpace(c.Title)
		}
		if r.Username == "" {
			r.Username = strings.TrimSpace(c.Username)
		}
		if r.Title != "" && r.Username != "" {
			break
		}
	}
	return r
}

// Display 群名；名字未知时退回数字 id。
func (r ChatRef) Display() string {
	if r.Title != "" {
		return r.Title
	}
	return strconv.FormatInt(r.ID, 10)
}

// URL 公开群的 t.me 链接；私有群（没有 @ 名）或未知时为空串。
func (r ChatRef) URL() string {
	if r.Username != "" {
		return "https://t.me/" + r.Username
	}
	return ""
}

// HTML 返回可直接放进 TG HTML 消息的群引用：公开群是带链接的群名，私有群
// 是群名，都没有则是数字 id。群名经 HTML 转义。
func (r ChatRef) HTML() string {
	name := html.EscapeString(r.Display())
	if u := r.URL(); u != "" {
		return `<a href="` + u + `">` + name + `</a>`
	}
	return name
}
