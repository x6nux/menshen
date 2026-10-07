package core

import (
	"fmt"
	"log/slog"
	"time"
)

// Role 是三档权限。
//
// 主管理员来自 config.admin_ids，**不进 admins 表** —— 配置文件是唯一
// 不可能被面板误操作删空的地方，最高权限必须固定在那里。否则一次误操作
// 就能让整个服务没有任何人能管。
type Role int

const (
	RoleNone Role = iota
	RoleSub       // 次级管理员：只碰自己名下的 bot 与群
	RoleMain      // 主管理员：上游、模型、联合封禁、全部 bot
)

func (r Role) String() string {
	switch r {
	case RoleSub:
		return "次级管理员"
	case RoleMain:
		return "主管理员"
	}
	return "普通用户"
}

// RoleOf 判定此人的角色。
func (sh *Shared) RoleOf(uid int64) Role {
	if sh.Cfg.IsAdmin(uid) {
		return RoleMain
	}
	if _, ok := sh.Cache.Snap().Admins[uid]; ok {
		return RoleSub
	}
	return RoleNone
}

func (sh *Shared) IsMain(uid int64) bool { return sh.RoleOf(uid) == RoleMain }

// IsStaff 报告此人是否能进管理面板（主管或次管）。
func (sh *Shared) IsStaff(uid int64) bool { return sh.RoleOf(uid) != RoleNone }

// CanManageBot 报告此人能否管理该 bot：主管理员全通，次管只限自己名下。
//
// 面板里每一个会改动状态的分支都要单独调它，不能只在导航层判一次：
// callback_data 是客户端发上来的，任何人都能把别人 bot 的 id 拼进去。
func (sh *Shared) CanManageBot(uid, botID int64) bool {
	if sh.IsMain(uid) {
		return true
	}
	r, ok := sh.Cache.Snap().Bots[botID]
	return ok && r.OwnerID == uid
}

// CanManageChat 报告此人能否管理某个 bot 在某群的配置。
// 群的归属完全由 bot 的归属决定 —— 群不是独立的权限主体。
func (sh *Shared) CanManageChat(uid, botID, chatID int64) bool {
	if !sh.CanManageBot(uid, botID) {
		return false
	}
	_, ok := sh.Cache.Snap().ChatConf(botID, chatID)
	return ok
}

// AddAdmin 添加次级管理员。
func (sh *Shared) AddAdmin(uid int64, note string, by int64) error {
	if sh.Cfg.IsAdmin(uid) {
		// 主管理员已经是最高权限，再写进表里只会让面板显示一条
		// 删不掉的记录（删了也不影响他的权限）。
		return fmt.Errorf("该用户已是主管理员")
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO admins (user_id,note,added_by,created_at)
		VALUES (?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET note=excluded.note`,
		uid, note, by, time.Now().Unix()); err != nil {
		slog.Error("添加次级管理员失败", "uid", uid, "err", err)
		return err
	}
	if err := sh.Cache.Reload(); err != nil {
		return err
	}
	// 立刻给新管理员补挂 Mini App 菜单按钮：按钮是 per-chat 的，必须由
	// bot 主动设置，等到下次重启才挂的话他点菜单也进不去配置台。
	// 异步：调用方卡在一条 TG 回调的响应路径上，而每个 bot 一次往返。
	if sh.Reg != nil {
		go sh.Reg.Each(func(b *Bot) { b.RegisterMiniAppButtonFor(uid) })
	}
	return nil
}

// RemoveAdmin 移除次级管理员。
//
// 他名下的 bot **不删也不停** —— 那些 bot 正在群里工作，权限变更不该
// 顺带让一批群失去防护。归属留在原处，主管理员可以在面板上改派或删除。
func (sh *Shared) RemoveAdmin(uid int64) error {
	if _, err := sh.Store.Write.Exec(`DELETE FROM admins WHERE user_id=?`, uid); err != nil {
		slog.Error("移除次级管理员失败", "uid", uid, "err", err)
		return err
	}
	return sh.Cache.Reload()
}
