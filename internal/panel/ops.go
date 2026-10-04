package panel

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"menshen/internal/core"
)

// ---- TG 面板与 Mini App 共用的小件 ----
//
// 写操作本身在数据所属的包里（core 的群/上游/模型、antiad 的规则与名单、
// 本包 ops_settings.go 的设置项）；这里只放两个界面各自把结果翻译给人看
// 的那一点。

func ptr[T any](v T) *T { return &v }

// opText 把操作失败翻译成 TG 面板上的一句话：OpError 原样给人看，
// 其余是内部故障，只回 generic、细节进日志。
func opText(err error, generic string) string {
	var op *core.OpError
	if errors.As(err, &op) {
		return op.Msg
	}
	slog.Error("面板：操作失败", "err", err)
	return generic
}

// miniFail 是 opText 的 Mini App 版：OpError 回 400 / 403，其余回 500。
func miniFail(w http.ResponseWriter, err error, generic string) {
	var op *core.OpError
	if errors.As(err, &op) {
		status := http.StatusBadRequest
		if op.Denied {
			status = http.StatusForbidden
		}
		miniErr(w, status, op.Msg)
		return
	}
	slog.Error("miniapp：操作失败", "err", err)
	miniErr(w, http.StatusInternalServerError, generic)
}

// chatTitle 用**负责这个群的那个 bot** 查群标题；实例不在运行或查询失败
// 返回空串。查不到不算失败 —— bot 还没进群就添加 chat_id 是合法的使用
// 顺序。不能用面板所在的 bot 查：那通常是不入群的主 bot，永远查不到。
func chatTitle(sh *core.Shared, botID, chatID int64) string {
	if sh.Reg == nil {
		return ""
	}
	b, ok := sh.Reg.LookupID(botID)
	if !ok {
		return ""
	}
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return ""
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Title string `json:"title"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return ""
	}
	return resp.Result.Title
}
