// 上游连通性测试：Mini App 上游详情页的「测试连通」按钮走后端
// upstream {action:"test"}，最终落到这里的 TestUpstream。
//
// 它复用判定链路的单次请求路径 aiAttempt —— 鉴权头、base_url 拼接、
// 看门狗与响应读取都与真实判定完全一致，避免「测试通过、判定失败」。
// 但不走 aiCall：那套重试与 150 秒总预算属于消息判定，管理员点一下按钮
// 不该触发多次计费请求、也不该等满预算。
package antiad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"menshen/internal/core"
	"menshen/internal/upstream"
)

// 配置类问题的哨兵错误：面板据此回 400（参数/状态不对），
// 其余失败（网络、鉴权、上游 5xx）回 200 + ok:false，由前端行内展示。
var (
	ErrUpstreamNotFound = errors.New("上游不存在")
	ErrUpstreamNoModel  = errors.New("该上游还没有登记可用模型")
	ErrUpstreamNoChat   = errors.New("该上游未开启 chat 能力，无法测试")
)

// TestUpstream 用该上游名下一个已登记的启用模型发一次最小 chat 请求，
// 验证 base_url / api_key / chat 能力是否可用。
//
// 返回实际使用的模型全名（<上游名>/<模型ID>）与请求耗时；失败时返回
// 哨兵错误（配置问题）或 aiAttempt 的可读错误（请求失败）。ctx 由调用方
// 控制整体超时。
func TestUpstream(ctx context.Context, sh *core.Shared, upstreamID int64) (string, time.Duration, error) {
	snap := sh.Cache.Snap()
	var up *upstream.Upstream
	for _, u := range snap.Upstreams {
		if u.ID == upstreamID {
			up = u
			break
		}
	}
	if up == nil {
		return "", 0, ErrUpstreamNotFound
	}
	if !up.Supports(upstream.EPChat) {
		return "", 0, ErrUpstreamNoChat
	}

	// 属于该上游且启用的模型。名字排序保证同一渠道每次测的是同一个，
	// 结果可复现；上游停用不影响测试 —— 管理员往往在启用前先测。
	names := make([]string, 0, 4)
	for name, m := range snap.Models {
		if m.Enabled && m.UpstreamName() == up.Name {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", 0, ErrUpstreamNoModel
	}
	sort.Strings(names)
	model := names[0]

	// 最小 chat 请求：一句话、最多 1 个 token。判定链路用的
	// session_id / 鉴权头由 aiAttempt 统一补上，这里不重复实现。
	payload := map[string]any{
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	}
	start := time.Now()
	r := aiAttempt(ctx, sh, snap, upstream.EPChat, model, up, payload)
	latency := time.Since(start)
	if r.err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return model, latency, fmt.Errorf("测试超时：上游在限定时间内没有响应")
		}
		return model, latency, r.err
	}

	// HTTP 200 还不够：有的网关会把错误页或代理页按 200 返回。要求它是
	// 形状正确的 chat completion —— 判定链路同样依赖 choices。
	var resp struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(r.reply.Raw, &resp); err != nil || len(resp.Choices) == 0 {
		return model, latency, fmt.Errorf("上游返回的不是 chat completion 响应：%s",
			core.TruncateRunes(string(r.reply.Raw), 200))
	}
	return model, latency, nil
}
