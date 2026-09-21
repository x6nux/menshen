package core

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"menshen/internal/config"
	"menshen/internal/tg"
)

// maxUpdateBytes 是单次 webhook 请求体的上限。
// Telegram 的 update 远小于它，设这个界是为了不让任意一个能连上监听端口
// 的东西用一条巨大的 POST 把内存吃光。
const maxUpdateBytes = 1 << 20 // 1 MiB

// tokenFromPath 从回调路径里取出 bot token。
//
// 不做前后缀的死板匹配，而是切段之后找出「长得像 token 的那一段」：
// 部署形态五花八门 —— <public_url>/<token>/webhook 是本项目的默认，
// 反代可能再套一层子路径，历史形态还有 /bot<TOKEN>。把识别锚在 token
// 自身的格式上，这些形态就全都不用单独适配。
//
// token 里没有斜杠，所以切段不会把它切碎。
func TokenFromPath(p string) string {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		if config.ValidToken(seg) {
			return seg
		}
		// 历史形态 /bot<TOKEN>
		if t, ok := strings.CutPrefix(seg, "bot"); ok && config.ValidToken(t) {
			return t
		}
	}
	return ""
}

// ServeHTTP 把一条 webhook 推送投递给对应 bot 的串行队列。
//
// 认证模型与 Telegram 官方的 /bot<TOKEN>/method 一致：路径里的 token
// 就是凭证。知道 token 的人本来就能直接控制那个 bot，伪造推送并不会
// 多出任何权限，所以不再叠一层 secret。
//
// 但接入是**注册制**：token 必须已由某个管理员登记过（见 Registry
// 的 register）。没登记的一律 401 —— 没有归属就谈不上谁能管它、
// 谁为它的开销负责。
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/healthz" {
		w.Write([]byte("ok"))
		return
	}
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := TokenFromPath(req.URL.Path)
	if token == "" {
		// 不回显路径：它含 token，而错误页可能被反代记进访问日志。
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	b, ok := r.LookupToken(token)
	if !ok {
		slog.Warn("webhook：未注册的 bot，已拒绝", "token", config.MaskToken(token))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var u tg.Update
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxUpdateBytes)).
		Decode(&u); err != nil {
		slog.Warn("webhook：请求体无法解析", "token", config.MaskToken(token), "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !b.Enqueue(&u) {
		// 丢弃而不是阻塞：阻塞会让 TG 投递超时并重推，重推又落到同一个
		// 满队列上。与「判定并发已满放行」是同一个失败方向——
		// 宁可漏一条，也不让投递链路反过来拖垮自己。
		slog.Warn("webhook：队列已满，丢弃一条更新",
			"token", config.MaskToken(token), "update_id", u.UpdateID)
	}

	// 无论投递成功与否都回 200：回非 2xx 会让 TG 反复重推同一条，
	// 而队列满的时候最不需要的就是更多重推。
	w.WriteHeader(http.StatusOK)
}
