package antiad

// 规则发现模型的网络重试：Eino 的 OpenAI 客户端自身不做重试，上游网关
// 偶发超时（Client.Timeout exceeded while awaiting headers）、5xx、429
// 会让整轮发现直接失败。这里给 ChatModel 包一层：请求失败后按策略重试。
//
// 策略（产品要求）：最多重试 10 次；前 5 次固定间隔，后 5 次指数退避。
// 只重试可能自愈的错误：超时、连接类错误、429 与 5xx；4xx（除 429）
// 与参数/序列化错误是确定性的，重试只会浪费预算。重试等待可被 ctx 取消
// （手动停止 / 30 分钟总时限），取消后立即把最后一次错误交给 ReAct 图。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"

	"menshen/internal/upstream"
)

// 重试策略参数是 var 而不是 const：测试要替换成不真等的实现，覆盖固定
// 间隔与指数退避两段以及不重试的分支。
var (
	// ruleAgentRetryCount 是最大重试次数（不含首次请求）。
	ruleAgentRetryCount = 10
	// ruleAgentRetryFixedCount 是固定间隔的重试次数，其余走指数退避。
	ruleAgentRetryFixedCount = 5
	// ruleAgentRetryFixed 是固定间隔。
	ruleAgentRetryFixed = 5 * time.Second
	// ruleAgentRetryBase 是指数退避的基数：第 6 次重试等它，之后依次翻倍。
	ruleAgentRetryBase = 5 * time.Second
	// ruleAgentRetrySleep 等待下一次重试；测试替换成立即返回并记录时长。
	ruleAgentRetrySleep = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

// retryDelay 返回第 retry 次重试（1-based）前的等待时长。
func ruleAgentRetryDelay(retry int) time.Duration {
	if retry <= ruleAgentRetryFixedCount {
		return ruleAgentRetryFixed
	}
	return ruleAgentRetryBase << (retry - ruleAgentRetryFixedCount - 1)
}

// apiErrorInfo 从错误链里取 HTTP 状态码与供应商消息。
//
// 同一件事有两套错误类型：SDK 的 RequestError（请求构造/传输层失败）与
// APIError（读到 HTTP 状态与 JSON 错误体），以及 Eino 的 OpenAI 组件把它
// 转成的自己的 APIError —— 那次转换是**改写**而不是包装，原错误不在链上
// （见 eino-ext/components/model/openai/types.go 的 convOrigAPIError），
// 所以只认 SDK 那套会在真实链路上漏判：可自愈的 429/5xx 被当成确定性
// 错误直接放弃，模型拒绝 temperature 的 400 也认不出来。
func apiErrorInfo(err error) (status int, msg string, ok bool) {
	var einoErr *einoopenai.APIError
	if errors.As(err, &einoErr) {
		return einoErr.HTTPStatusCode, einoErr.Message, true
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode, apiErr.Message, true
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode, reqErr.Error(), true
	}
	return 0, "", false
}

// retryableModelError 判断一个模型请求错误是否值得重试。
func retryableModelError(err error) bool {
	if err == nil {
		return false
	}
	// 调用方主动取消（手动停止）绝不重试。
	if errors.Is(err, context.Canceled) {
		return false
	}
	// HTTP 错误按状态码分类：429 与 5xx 可重试，其余 4xx 是确定性的。
	if status, _, ok := apiErrorInfo(err); ok {
		return status == http.StatusTooManyRequests || status >= 500
	}
	// 传输层错误：超时、连接重置、DNS 抖动等都算 net.Error。
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// 客户端超时（http.Client.Timeout）在错误链上就是 DeadlineExceeded；
	// 此时请求 ctx 可能还活着（超时来自客户端自己的 deadline）。
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// 连接被对端掐断时的裸 EOF。
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

// retryChatModel 给 ToolCallingChatModel 加网络重试。Generate 整段重试；
// Stream 只重试建立阶段，流一旦返回，消费中的错误无法透明重试。
//
// 另有一类不是网络故障的失败也在这里就地自愈：上游拒绝 temperature 参数
// （见 upstream.TemperatureUnsupported）。Eino 把 temperature 写在模型配置
// 里，建好之后改不掉，所以在传输层按模型去掉该参数 —— 这里记下模型后立刻
// 重发一次，重发会经过那条去掉参数的路。
type retryChatModel struct {
	inner   model.ToolCallingChatModel
	modelID string // 发给上游的模型名，用于登记「不接受 temperature」
	// stripped 表示本实例已经为 temperature 重发过一次：重发仍然被拒说明
	// 去参数没生效（例如上游文案里另有 temperature 字样），再快速重发没有
	// 意义，交回常规重试与失败处理。
	stripped bool
}

// withModelRetry 包装模型；nil 原样返回。
func withModelRetry(inner model.ToolCallingChatModel, modelID string) model.ToolCallingChatModel {
	if inner == nil {
		return nil
	}
	return &retryChatModel{inner: inner, modelID: modelID}
}

func (m *retryChatModel) Generate(ctx context.Context, in []*schema.Message,
	opts ...model.Option) (*schema.Message, error) {

	var lastErr error
	for attempt := 0; ; attempt++ {
		out, err := m.inner.Generate(ctx, in, opts...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		// 模型不接受 temperature：记下来，下一轮传输层会去掉它，这里立即
		// 重发 —— 参数问题重发就解决，等 5 秒退避纯属浪费。
		if m.noteTemperatureRejection(err) {
			continue
		}
		if attempt >= ruleAgentRetryCount || !retryableModelError(err) || ctx.Err() != nil {
			return nil, lastErr
		}
		if err := m.waitRetry(ctx, attempt+1, lastErr); err != nil {
			return nil, lastErr
		}
	}
}

func (m *retryChatModel) Stream(ctx context.Context, in []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {

	var lastErr error
	for attempt := 0; ; attempt++ {
		out, err := m.inner.Stream(ctx, in, opts...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if m.noteTemperatureRejection(err) {
			continue
		}
		if attempt >= ruleAgentRetryCount || !retryableModelError(err) || ctx.Err() != nil {
			return nil, lastErr
		}
		if err := m.waitRetry(ctx, attempt+1, lastErr); err != nil {
			return nil, lastErr
		}
	}
}

func (m *retryChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &retryChatModel{inner: inner, modelID: m.modelID, stripped: m.stripped}, nil
}

// noteTemperatureRejection 报告这次失败是否为「模型不接受 temperature」。
// 是则记下模型并返回 true，让调用方立即重发；同一个实例只认一次。
func (m *retryChatModel) noteTemperatureRejection(err error) bool {
	if m.stripped {
		return false
	}
	status, msg, ok := apiErrorInfo(err)
	if !ok || !upstream.TemperatureRejected(status, msg) {
		return false
	}
	m.stripped = true
	upstream.MarkTemperatureUnsupported(m.modelID)
	slog.Warn("规则发现：模型不接受 temperature 参数，去掉后重试",
		"模型", m.modelID, "状态", status)
	return true
}

// waitRetry 等一次重试；等待期间被取消时返回 ctx 错误，调用方立即收尾。
func (m *retryChatModel) waitRetry(ctx context.Context, retry int, err error) error {
	delay := ruleAgentRetryDelay(retry)
	slog.Warn("规则发现：模型请求失败，准备重试",
		"重试", retry, "最多", ruleAgentRetryCount, "等待", delay, "err", err)
	return ruleAgentRetrySleep(ctx, delay)
}
