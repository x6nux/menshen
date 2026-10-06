package config

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// testToken 是格式合法但完全虚构的 token。
const testToken = "123456789:AAEEfaketoken_ForUnitTestsOnly1234567"

// writeConfig 把若干行写进临时 config.yaml 并返回路径。
// 每个用例都补上 bot_token 与 admin_ids，否则 Load 会先因缺它们而失败。
func writeConfig(t *testing.T, lines string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "bot_token: \"" + testToken + "\"\nadmin_ids:\n  - 1\n" + lines
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return p
}

func TestProxyParsed(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
tg_proxy: "http://127.0.0.1:7890"
ai_proxy: "socks5://10.0.0.1:1080"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TGProxy == nil || cfg.TGProxy.String() != "http://127.0.0.1:7890" {
		t.Errorf("tg_proxy = %v, 期望 http://127.0.0.1:7890", cfg.TGProxy)
	}
	if cfg.AIProxy == nil || cfg.AIProxy.Scheme != "socks5" {
		t.Errorf("ai_proxy = %v, 期望 socks5 代理", cfg.AIProxy)
	}
}

// TestProxyEmptyMeansEnv 锁住「留空 = 沿用环境变量」。
// 改成「留空即直连」会让现在靠 HTTPS_PROXY 跑着的部署升级后突然断网，
// 而表现只是一条连接超时。
func TestProxyEmptyMeansEnv(t *testing.T) {
	cfg, err := Load(writeConfig(t, "tg_proxy: \"\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TGProxy != nil || cfg.AIProxy != nil {
		t.Errorf("留空应当是 nil（交给环境变量），得到 tg=%v ai=%v",
			cfg.TGProxy, cfg.AIProxy)
	}
}

// TestProxyRejectsSchemeless 是这个功能最容易踩的坑，两类要分别挡：
//
//   - "127.0.0.1:7890" 由 url.Parse 自己报错（scheme 不能以数字开头）
//   - "localhost:1080" **不报错**，Scheme 会变成 "localhost"、Host 为空，
//     http.ProxyURL 拿着它既连不上也不吭声 —— 只有 scheme 白名单挡得住
//
// 漏掉后一类的话，表现是所有出网请求超时而配置文件看着完全正常。
func TestProxyRejectsSchemeless(t *testing.T) {
	for _, bad := range []string{"127.0.0.1:7890", "localhost:1080", "proxy:8080", "://x"} {
		if _, err := Load(writeConfig(t, "tg_proxy: \""+bad+"\"\n")); err == nil {
			t.Errorf("tg_proxy=%q 应当在启动时被拒", bad)
		}
	}
}

// TestProxyRejectsUnknownScheme 确认只认 HTTP 家族与 SOCKS5。
// 写错 scheme 的后果与上面一样：连不上，且不报错。
func TestProxyRejectsUnknownScheme(t *testing.T) {
	if _, err := Load(writeConfig(t, "ai_proxy: \"ftp://1.2.3.4:21\"\n")); err == nil {
		t.Error("ftp:// 不是可用的代理协议，应当被拒")
	}
}

// TestProxyTransportNilKeepsEnv 确认 nil 时返回 nil RoundTripper ——
// http.Client 的 Transport 为 nil 才会落到 DefaultTransport，
// 而后者的 Proxy 正是 ProxyFromEnvironment。
func TestProxyTransportNilKeepsEnv(t *testing.T) {
	if rt := ProxyTransport(nil); rt != nil {
		t.Errorf("nil 代理应当返回 nil RoundTripper，得到 %T", rt)
	}
}

// TestProxyTransportUsesURL 确认构造出来的 Transport 真的指向那个代理，
// 且没有丢掉 DefaultTransport 的连接池设置。
func TestProxyTransportUsesURL(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:7890")
	rt := ProxyTransport(u)
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("期望 *http.Transport，得到 %T", rt)
	}

	got, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.telegram.org"}})
	if err != nil {
		t.Fatalf("Proxy(): %v", err)
	}
	if got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Errorf("代理地址 = %v, 期望 http://127.0.0.1:7890", got)
	}

	// Clone 自 DefaultTransport，连接池等调优必须还在。
	// 直接 &http.Transport{Proxy: …} 会把它们全部清零，
	// 表现是高频请求下连接不复用、句柄涨得飞快。
	if tr.MaxIdleConns == 0 || tr.IdleConnTimeout == 0 {
		t.Errorf("丢掉了 DefaultTransport 的连接池设置: MaxIdleConns=%d IdleConnTimeout=%v",
			tr.MaxIdleConns, tr.IdleConnTimeout)
	}
}

// ---- 环境变量配置 ----

// TestEnvFillsAll 确认每一项都能只靠环境变量配出来。
func TestEnvFillsAll(t *testing.T) {
	t.Setenv("MENSHEN_BOT_TOKEN", testToken)
	t.Setenv("MENSHEN_ADMIN_IDS", "11, 22,33")
	t.Setenv("MENSHEN_TG_API_BASE", "https://tg.example.com/api/")
	t.Setenv("MENSHEN_DB_PATH", "/app/data/data.db")
	t.Setenv("MENSHEN_PUBLIC_URL", "https://ad.example.com")
	t.Setenv("MENSHEN_LISTEN_ADDR", "0.0.0.0:8081")
	t.Setenv("MENSHEN_TG_PROXY", "http://127.0.0.1:7890")
	t.Setenv("MENSHEN_AI_PROXY", "socks5://127.0.0.1:1080")
	t.Setenv("MENSHEN_CAPTCHA_PROVIDER", "hcaptcha")
	t.Setenv("MENSHEN_CAPTCHA_SITE_KEY", "site")
	t.Setenv("MENSHEN_CAPTCHA_SECRET", "secret")
	t.Setenv("MENSHEN_CAPTCHA_MIN_SCORE", "60")

	cfg, err := Load(filepath.Join(t.TempDir(), "不存在.yaml"))
	if err != nil {
		t.Fatalf("纯环境变量应当能启动: %v", err)
	}
	if cfg.BotToken != testToken {
		t.Errorf("bot_token = %q", cfg.BotToken)
	}
	if len(cfg.AdminIDs) != 3 || cfg.AdminIDs[0] != 11 || cfg.AdminIDs[2] != 33 {
		t.Errorf("admin_ids = %v, 期望 [11 22 33]（逗号分隔，容忍空格）", cfg.AdminIDs)
	}
	// 与文件路径一致地去掉尾部斜杠，否则拼出双斜杠。
	if cfg.TGAPIBase != "https://tg.example.com/api" {
		t.Errorf("tg_api_base = %q", cfg.TGAPIBase)
	}
	if cfg.DBPath != "/app/data/data.db" || cfg.ListenAddr != "0.0.0.0:8081" {
		t.Errorf("db_path = %q listen_addr = %q", cfg.DBPath, cfg.ListenAddr)
	}
	if !cfg.UseWebhook() {
		t.Error("配了 public_url 应当是 webhook 模式")
	}
	if cfg.TGProxy == nil || cfg.AIProxy == nil {
		t.Errorf("代理没读到: tg=%v ai=%v", cfg.TGProxy, cfg.AIProxy)
	}
	if cfg.CaptchaProvider != "hcaptcha" || !cfg.CaptchaEnabled() {
		t.Errorf("入群验证配置没读全: %+v", cfg)
	}
	if cfg.CaptchaMinScore != 60 {
		t.Errorf("captcha_min_score = %d, 期望 60", cfg.CaptchaMinScore)
	}
}

// TestCaptchaConfigValidation：提供方白名单与「cap 无需配置」。
//
// 拼错的提供方若被静默忽略，入群验证会「开着但不生效」，而面板上
// 一切正常 —— 正是最难查的那类事故，所以启动时就报错。
func TestCaptchaConfigValidation(t *testing.T) {
	if _, err := Load(writeConfig(t, "captcha_provider: \"recaptch\"\n")); err == nil {
		t.Error("未知的 captcha_provider 应当启动报错")
	}
	if _, err := Load(writeConfig(t, "captcha_provider: \"recaptcha\"\n")); err == nil {
		t.Error("已移除的 reCAPTCHA 应当启动报错")
	}
	if _, err := Load(writeConfig(t, "captcha_min_score: \"101\"\n")); err == nil {
		t.Error("越界的 captcha_min_score 应当启动报错")
	}

	// cap 是内置实现：无需 site key / secret / endpoint 即视为可用。
	cfg, err := Load(writeConfig(t, "captcha_provider: \"CAP\"\n"))
	if err != nil {
		t.Fatalf("提供方名应大小写不敏感: %v", err)
	}
	if cfg.CaptchaProvider != "cap" || !cfg.CaptchaEnabled() {
		t.Error("cap 无需任何密钥即应视为已启用")
	}

	// turnstile / hcaptcha 缺 secret 时按未启用处理（不报错）。
	cfg, err = Load(writeConfig(t, "captcha_provider: \"hcaptcha\"\ncaptcha_site_key: \"s\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CaptchaEnabled() {
		t.Error("hcaptcha 缺 secret 时不应视为已启用")
	}
}

// TestParseCaptchaDemoKeys：紧凑格式解析与错误分支。
func TestParseCaptchaDemoKeys(t *testing.T) {
	got, err := parseCaptchaDemoKeys(
		"turnstile=sk1,sec1; hcaptcha=sk2,sec2; cap=ignored,ignored")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got["turnstile"].SiteKey != "sk1" || got["turnstile"].Secret != "sec1" {
		t.Errorf("turnstile 解析错误: %+v", got["turnstile"])
	}
	if got["hcaptcha"].SiteKey != "sk2" {
		t.Errorf("hcaptcha 解析错误: %+v", got["hcaptcha"])
	}
	if _, ok := got["cap"]; ok {
		t.Error("cap 是内置实现，不该进演示密钥表")
	}

	for _, bad := range []string{
		"recaptcha=a,b",  // 已移除
		"turnstile=sk1",  // 缺 secret
		"turnstile=,sec", // 缺 site key
		"turnstile=a,b,c",
	} {
		if _, err := parseCaptchaDemoKeys(bad); err == nil {
			t.Errorf("%q 应当报错", bad)
		}
	}
}

// TestEnvOverridesFile 锁住优先级：环境变量压过配置文件。
//
// 容器里最常见的形态是「镜像里烤了一份 config.yaml，用环境变量覆盖个别
// 项」。反过来的话环境变量看着配了却不生效，而这个问题只在容器里出现，
// 本地怎么跑都复现不了。
func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("MENSHEN_DB_PATH", "/app/data/env.db")
	t.Setenv("MENSHEN_ADMIN_IDS", "999")

	cfg, err := Load(writeConfig(t, "db_path: \"file.db\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != "/app/data/env.db" {
		t.Errorf("db_path = %q, 环境变量应当压过文件里的 file.db", cfg.DBPath)
	}
	if len(cfg.AdminIDs) != 1 || cfg.AdminIDs[0] != 999 {
		t.Errorf("admin_ids = %v, 环境变量应当整体替换而不是追加", cfg.AdminIDs)
	}
}

// TestEnvValidatedSameAsFile 确认环境变量里的值走同一套校验。
// 少了这道，容器里配错代理的表现就退回成「所有请求超时」。
func TestEnvValidatedSameAsFile(t *testing.T) {
	t.Setenv("MENSHEN_BOT_TOKEN", testToken)
	t.Setenv("MENSHEN_ADMIN_IDS", "1")
	t.Setenv("MENSHEN_TG_PROXY", "localhost:1080")

	if _, err := Load(filepath.Join(t.TempDir(), "无.yaml")); err == nil {
		t.Error("环境变量里的非法代理地址也必须在启动时被拒")
	}
}

// TestMissingFileWithoutEnvStillFails 确认「文件不存在」没有把必填项的
// 校验一起放过 —— 否则配置路径写错会表现成一个莫名其妙的运行时错误。
func TestMissingFileWithoutEnvStillFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "无.yaml")); err == nil {
		t.Error("既没有文件也没有环境变量时应当报错")
	}
}

// TestTGAPIDefaults：tg_api_id/tg_api_hash 不配时走内置默认值（入群时间
// 回查开箱可用）；配了就用自己的；配 0 或空串则关闭这一项。
func TestTGAPIDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.TGAPIID != defaultTGAPIID || cfg.TGAPIHash != defaultTGAPIHash {
		t.Errorf("不配时应走内置默认值，得到 %d/%q", cfg.TGAPIID, cfg.TGAPIHash)
	}

	c2, err := Load(writeConfig(t, "tg_api_id: 1234\ntg_api_hash: \"myhash\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.TGAPIID != 1234 || c2.TGAPIHash != "myhash" {
		t.Errorf("显式配置应生效，得到 %d/%q", c2.TGAPIID, c2.TGAPIHash)
	}

	c3, err := Load(writeConfig(t, "tg_api_id: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c3.TGAPIID != 0 {
		t.Errorf("tg_api_id: 0 应关闭回查，得到 %d", c3.TGAPIID)
	}

	c4, err := Load(writeConfig(t, "tg_api_id: \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c4.TGAPIID != 0 {
		t.Errorf("空串应关闭回查，得到 %d", c4.TGAPIID)
	}
}
