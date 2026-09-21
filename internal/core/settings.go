package core

import (
	"encoding/json"
	"log/slog"
)

// ---- 设置持久化 ----
//
// 这四个函数挂在 Shared 上而不属于面板：设置是全局一份，形态总结这类
// 定时任务、判定链路都要写它。放进 panel 会让 antiad 反向依赖 panel。

// putSetting 写入单项全局设置并刷新快照。settings 是 KV 表，用 UPSERT 覆盖。
//
// 挂在 shared 上：设置是全局一份，形态总结这类定时任务也要写它。
func (sh *Shared) PutSetting(k, v string) error {
	_, err := sh.Store.Write.Exec(
		`INSERT INTO settings (k,v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	if err != nil {
		slog.Error("写入设置失败", "key", k, "err", err)
		return err
	}
	if err := sh.Cache.Reload(); err != nil {
		slog.Error("写入设置后 reload 失败", "key", k, "err", err)
		return err
	}
	return nil
}

func (sh *Shared) PutInt64List(k string, list []int64) error {
	if list == nil {
		list = []int64{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return sh.PutSetting(k, string(raw))
}

// putBotSetting 写一条 bot 级覆盖。值与全局默认相同时删除这条覆盖 ——
// 留着的话，主管理员之后调整全局默认，这个 bot 会莫名其妙地不跟随。
func (sh *Shared) PutBotSetting(botID int64, k, v string) error {
	if v == sh.Cache.Snap().Settings[k] {
		if _, err := sh.Store.Write.Exec(
			`DELETE FROM bot_settings WHERE bot_id=? AND k=?`, botID, k); err != nil {
			return err
		}
		return sh.Cache.Reload()
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO bot_settings (bot_id,k,v)
		VALUES (?,?,?) ON CONFLICT(bot_id,k) DO UPDATE SET v=excluded.v`,
		botID, k, v); err != nil {
		slog.Error("写入 bot 设置失败", "bot_id", botID, "key", k, "err", err)
		return err
	}
	return sh.Cache.Reload()
}

func (sh *Shared) PutBotInt64List(botID int64, k string, list []int64) error {
	if list == nil {
		list = []int64{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return sh.PutBotSetting(botID, k, string(raw))
}
