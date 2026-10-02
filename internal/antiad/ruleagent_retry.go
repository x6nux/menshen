package antiad

// 规则发现模型的网络重试：Eino 的 OpenAI 客户端自身不做重试，上游网关
// 偶发超时（Client.Timeout exceeded while awaiting headers）、5xx、429
// 会让整轮发现直接失败。这里给 ChatModel 包一层：请求失败后按策略重试。
//
// 策略（产品要求）：最多重试 10 次；前 5 次固定间隔，后 5 次指数退避。
// 只重试「可能自愈」的错误：超时、连接类错误、429 与 5xx；4xx（除 429）
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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

// 重试策略参数是 var 而不是 const：测试要替换成不真等的实现，覆盖固定
// 间隔与指数退避两段以及「不重试」的分支。
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
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode == http.StatusTooManyRequests ||
			reqErr.HTTPStatusCode >= 500
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
// Stream 只重试「建立阶段」，流一旦返回，消费中的错误无法透明重试。
type retryChatModel struct {
	inner model.ToolCallingChatModel
}

// withModelRetry 包装模型；nil 原样返回。
func withModelRetry(inner model.ToolCallingChatModel) model.ToolCallingChatModel {
	if inner == nil {
		return nil
	}
	return &retryChatModel{inner: inner}
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
	return &retryChatModel{inner: inner}, nil
}

// waitRetry 等一次重试；等待期间被取消时返回 ctx 错误，调用方立即收尾。
func (m *retryChatModel) waitRetry(ctx context.Context, retry int, err error) error {
	delay := ruleAgentRetryDelay(retry)
	slog.Warn("规则发现：模型请求失败，准备重试",
		"重试", retry, "最多", ruleAgentRetryCount, "等待", delay, "err", err)
	return ruleAgentRetrySleep(ctx, delay)
}
