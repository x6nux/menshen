package core

import (
	"slices"
	"strings"
	"time"
)

// ---- 生效群配置（bot_chats）的写操作 ----
//
// TG 面板与 Mini App 共用这一份：校验与默认值只能有一处，否则两个界面
// 迟早各长出一套规则。

// AddChat 把一个群加到某个 bot 名下。新群默认**演练**：判定照常执行但
// 不作用于群内，确认无误伤后再切正式。已存在时只补标题（空标题不覆盖已有）。
func (sh *Shared) AddChat(botID, chatID int64, title string) error {
	if chatID == 0 {
		return Bad("chat_id 无效")
	}
	if rec := sh.Cache.Snap().Bots[botID]; rec != nil && rec.IsMain {
		// 主 bot 的群配置会在启动时被 ensureMainBot 清掉，加进去也只会立刻消失。
		return Bad("主 bot 不入群、不判定，不能添加生效群")
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO bot_chats
		(bot_id,chat_id,title,enabled,dryrun,group_alert,created_at)
		VALUES (?,?,?,1,1,0,?)
		ON CONFLICT(bot_id,chat_id) DO UPDATE SET title=excluded.title
		WHERE excluded.title<>''`,
		botID, chatID, title, time.Now().Unix()); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// RemoveChat 把群从 bot 名下移除。
func (sh *Shared) RemoveChat(botID, chatID int64) error {
	if _, err := sh.Store.Write.Exec(
		`DELETE FROM bot_chats WHERE bot_id=? AND chat_id=?`, botID, chatID); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// ChatPatch 是群配置的部分更新：nil 字段不动（false / 0 都是合法值，
// 不能拿零值当没传）。
type ChatPatch struct {
	Enabled, Dryrun, GroupAlert *bool
	Punish                      *int64 // -1 跟随 bot 设置 / 0 禁言 / 1 封禁
	Title                       *string
}

// UpdateChats 把同一份补丁应用到 bot 名下的若干群，返回实际命中的群数。
// 不在名下的 chat_id 自然跳过（WHERE bot_id=?），重复的只算一次。
func (sh *Shared) UpdateChats(botID int64, chatIDs []int64, p ChatPatch) (int64, error) {
	var cols []string
	var args []any
	for _, f := range []struct {
		col string
		v   *bool
	}{{"enabled", p.Enabled}, {"dryrun", p.Dryrun}, {"group_alert", p.GroupAlert}} {
		if f.v != nil {
			cols = append(cols, f.col+"=?")
			args = append(args, boolInt(*f.v))
		}
	}
	if p.Punish != nil {
		if *p.Punish < -1 || *p.Punish > 1 {
			return 0, Bad("punish 只能是 -1（跟随）、0（禁言）、1（封禁）")
		}
		cols = append(cols, "punish=?")
		args = append(args, *p.Punish)
	}
	if p.Title != nil {
		cols = append(cols, "title=?")
		args = append(args, *p.Title)
	}
	if len(cols) == 0 {
		return 0, nil
	}
	// cols 只来自上面的字面量，不存在注入面。
	q := `UPDATE bot_chats SET ` + strings.Join(cols, ",") + ` WHERE bot_id=? AND chat_id=?`
	ids := slices.Clone(chatIDs)
	slices.Sort(ids)
	var n int64
	for _, id := range slices.Compact(ids) {
		res, err := sh.Store.Write.Exec(q, append(args, botID, id)...)
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, sh.Cache.Reload()
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
