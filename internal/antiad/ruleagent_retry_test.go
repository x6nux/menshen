package antiad

// 规则发现模型重试的单元测试：用可编排的假模型覆盖固定间隔、指数退避、
// 重试上限、不重试分支（4xx/取消）与 Stream 建立阶段。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

// fakeRetryModel 按脚本返回错误：第 n 次调用返回 errs[n-1]，脚本耗尽后成功。
type fakeRetryModel struct {
	mu    sync.Mutex
	calls int
	errs  []error
}

func (f *fakeRetryModel) next() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	return nil
}

func (f *fakeRetryModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRetryModel) Generate(ctx context.Context, in []*schema.Message,
	opts ...model.Option) (*schema.Message, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (f *fakeRetryModel) Stream(ctx context.Context, in []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "ok"}}), nil
}

func (f *fakeRetryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

// timeoutErr 造一个与线上一致的客户端超时错误（url.Error 包装 DeadlineExceeded）。
func timeoutErr() error {
	return &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
		Err: context.DeadlineExceeded}
}

// setupRetryTest 把重试参数缩到可断言的小值，并替换等待实现。
func setupRetryTest(t *testing.T) *[]time.Duration {
	t.Helper()
	origCount, origFixedCount := ruleAgentRetryCount, ruleAgentRetryFixedCount
	origFixed, origBase, origSleep := ruleAgentRetryFixed, ruleAgentRetryBase, ruleAgentRetrySleep
	ruleAgentRetryFixed = time.Second
	ruleAgentRetryBase = time.Second
	sleeps := &[]time.Duration{}
	ruleAgentRetrySleep = func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return nil
	}
	t.Cleanup(func() {
		ruleAgentRetryCount, ruleAgentRetryFixedCount = origCount, origFixedCount
		ruleAgentRetryFixed, ruleAgentRetryBase, ruleAgentRetrySleep = origFixed, origBase, origSleep
	})
	return sleeps
}

// TestRetryChatModelFixedThenExponential：10 次重试全部失败后第 11 次成功；
// 等待序列是 5 次固定 + 5 次指数（1s 基数下 1,1,1,1,1,1,2,4,8,16）。
func TestRetryChatModelFixedThenExponential(t *testing.T) {
	sleeps := setupRetryTest(t)
	errs := make([]error, 10)
	for i := range errs {
		errs[i] = timeoutErr()
	}
	inner := &fakeRetryModel{errs: errs}

	out, err := withModelRetry(inner).Generate(context.Background(), nil)
	if err != nil || out == nil {
		t.Fatalf("第 11 次应成功：out=%v err=%v", out, err)
	}
	if inner.callCount() != 11 {
		t.Errorf("应请求 11 次（1 首次 + 10 重试），得到 %d", inner.callCount())
	}
	want := []time.Duration{
		time.Second, time.Second, time.Second, time.Second, time.Second,
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
	}
	if len(*sleeps) != len(want) {
		t.Fatalf("等待次数应为 %d，得到 %v", len(want), *sleeps)
	}
	for i := range want {
		if (*sleeps)[i] != want[i] {
			t.Errorf("第 %d 次等待应为 %v，得到 %v", i+1, want[i], (*sleeps)[i])
		}
	}
}

// TestRetryChatModelStopsAtLimit：重试 10 次仍失败就返回最后一次错误。
func TestRetryChatModelStopsAtLimit(t *testing.T) {
	sleeps := setupRetryTest(t)
	errs := make([]error, 20)
	for i := range errs {
		errs[i] = timeoutErr()
	}
	inner := &fakeRetryModel{errs: errs}

	if _, err := withModelRetry(inner).Generate(context.Background(), nil); err == nil {
		t.Fatal("重试耗尽应返回错误")
	}
	if inner.callCount() != 11 || len(*sleeps) != 10 {
		t.Errorf("应 11 次请求 / 10 次等待，得到 %d / %d", inner.callCount(), len(*sleeps))
	}
}

// TestRetryChatModelSkipsDeterministic4xx：400 是确定性错误，不重试。
func TestRetryChatModelSkipsDeterministic4xx(t *testing.T) {
	sleeps := setupRetryTest(t)
	inner := &fakeRetryModel{errs: []error{
		&openai.RequestError{HTTPStatusCode: http.StatusBadRequest,
			HTTPStatus: "400 Bad Request", Err: errors.New("invalid request")},
	}}

	if _, err := withModelRetry(inner).Generate(context.Background(), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if inner.callCount() != 1 || len(*sleeps) != 0 {
		t.Errorf("400 不该重试，得到请求 %d 次 / 等待 %d 次", inner.callCount(), len(*sleeps))
	}
}

// TestRetryChatModelRetries429And5xx：限流与上游故障可重试。
func TestRetryChatModelRetries429And5xx(t *testing.T) {
	sleeps := setupRetryTest(t)
	inner := &fakeRetryModel{errs: []error{
		&openai.RequestError{HTTPStatusCode: http.StatusTooManyRequests, Err: errors.New("rate limited")},
		&openai.RequestError{HTTPStatusCode: http.StatusBadGateway, Err: errors.New("bad gateway")},
	}}

	if _, err := withModelRetry(inner).Generate(context.Background(), nil); err != nil {
		t.Fatalf("429/502 后应重试成功：%v", err)
	}
	if inner.callCount() != 3 || len(*sleeps) != 2 {
		t.Errorf("应 3 次请求 / 2 次等待，得到 %d / %d", inner.callCount(), len(*sleeps))
	}
}

// TestRetryChatModelStopsOnCancel：调用方取消时不重试。
func TestRetryChatModelStopsOnCancel(t *testing.T) {
	sleeps := setupRetryTest(t)
	inner := &fakeRetryModel{errs: []error{fmt.Errorf("wrap: %w", context.Canceled)}}

	if _, err := withModelRetry(inner).Generate(context.Background(), nil); err == nil {
		t.Fatal("应返回错误")
	}
	if inner.callCount() != 1 || len(*sleeps) != 0 {
		t.Errorf("取消不该重试，得到请求 %d 次 / 等待 %d 次", inner.callCount(), len(*sleeps))
	}
}

// TestRetryChatModelCancelDuringWait：等待重试期间被取消，立即收尾。
func TestRetryChatModelCancelDuringWait(t *testing.T) {
	_ = setupRetryTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	ruleAgentRetrySleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	inner := &fakeRetryModel{errs: []error{timeoutErr(), timeoutErr()}}

	if _, err := withModelRetry(inner).Generate(ctx, nil); err == nil {
		t.Fatal("取消后应返回错误")
	}
	if inner.callCount() != 1 {
		t.Errorf("等待中被取消不该再发请求，得到 %d 次", inner.callCount())
	}
}

// TestRetryChatModelStreamRetriesCreation：Stream 的建立阶段同样重试；
// WithTools 后的模型仍保留重试。
func TestRetryChatModelStreamRetriesCreation(t *testing.T) {
	sleeps := setupRetryTest(t)
	inner := &fakeRetryModel{errs: []error{timeoutErr()}}
	m, err := withModelRetry(inner).WithTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stream(context.Background(), nil); err != nil {
		t.Fatalf("重试后应建立流：%v", err)
	}
	if inner.callCount() != 2 || len(*sleeps) != 1 {
		t.Errorf("应 2 次请求 / 1 次等待，得到 %d / %d", inner.callCount(), len(*sleeps))
	}
}

// TestRetryChatModelRetriesAPIError429：网关回整包 429 时 SDK 产生的是
// APIError 而不是 RequestError，同样必须重试（线上踩过：整轮 7 秒收场）。
func TestRetryChatModelRetriesAPIError429(t *testing.T) {
	sleeps := setupRetryTest(t)
	inner := &fakeRetryModel{errs: []error{
		&openai.APIError{HTTPStatusCode: http.StatusTooManyRequests,
			Message: "当前分组上游负载已饱和，请稍后再试"},
	}}

	if _, err := withModelRetry(inner).Generate(context.Background(), nil); err != nil {
		t.Fatalf("APIError 429 后应重试成功：%v", err)
	}
	if inner.callCount() != 2 || len(*sleeps) != 1 {
		t.Errorf("应 2 次请求 / 1 次等待，得到 %d / %d", inner.callCount(), len(*sleeps))
	}
}

// TestRetryableModelError 锁住分类边界。
func TestRetryableModelError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"超时", timeoutErr(), true},
		{"连接被拒", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"io.EOF", fmt.Errorf("read: %w", io.EOF), true},
		{"429", &openai.RequestError{HTTPStatusCode: 429}, true},
		{"500", &openai.RequestError{HTTPStatusCode: 500}, true},
		{"400", &openai.RequestError{HTTPStatusCode: 400}, false},
		{"APIError 429", &openai.APIError{HTTPStatusCode: 429, Message: "rate limited"}, true},
		{"APIError 503", &openai.APIError{HTTPStatusCode: 503, Message: "unavailable"}, true},
		{"APIError 400", &openai.APIError{HTTPStatusCode: 400, Message: "bad request"}, false},
		{"取消", context.Canceled, false},
		{"未知错误", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := retryableModelError(c.err); got != c.want {
			t.Errorf("%s：want %v got %v", c.name, c.want, got)
		}
	}
}
