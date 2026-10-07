package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// defaultTGAPIBase 是 Telegram Bot API 的默认基址。
//
// 默认走反代而不是官方地址：直连 api.telegram.org 在部分网络环境下连
// TCP 握手都会超时，而它是整个 bot 唯一的输入通道，不通等于服务没启动。
// 想直连或换自建反代，在 config.yaml 里配 tg_api_base 覆盖即可。
const defaultTGAPIBase = "https://833337.xyz/_tg/api"

type Config struct {
	BotToken string
	AdminIDs []int64
	// TGAPIBase 是 Bot API 基址，不含尾部斜杠。
	// 它必须留在配置文件里而不能进管理面板：面板本身要靠它才连得上 TG。
	TGAPIBase string
	DBPath    string // 默认 ./data.db

	// PublicURL 非空即启用 webhook 模式，否则走长轮询。
	//
	// 它是**基址**而不是完整回调地址：真正的回调地址是
	// <public_url>/<bot_token>/webhook。前缀可以带任意子路径，
	// 方便挂在反代的某个位置下（https://域名/某段/<token>/webhook）。
	// 每个接入的 bot 都按这个形态注册，token 在路径里，本进程据此分发。
	//
	// 必须是 Telegram 能访问到的 **https** 地址（TG 不接受裸 http），
	// 通常是反代对外的地址；本进程自己只在 ListenAddr 上收明文 HTTP。
	PublicURL string
	// ListenAddr 仅 webhook 模式使用。默认只听回环——
	// 这个端口上的请求等同于以 bot 身份收发消息，直接暴露到公网
	// 等于把 bot 的控制面摆在门口。
	ListenAddr string

	// TGProxy / AIProxy 是两类出网请求各自的代理，nil 表示不指定 ——
	// 此时沿用 HTTP_PROXY / HTTPS_PROXY 环境变量（Go 的默认行为）。
	//
	// 分开配而不是共用一个：两类请求的出网路径常不同，共用会让无需绕行的
	// 流量多一跳。两者都留空则两者都读环境变量。
	//
	// 代理不是换 API 地址的替代品：tg_api_base 换的是目标地址，
	// 代理换的是到达路径。反代通常比代理更省事，见 defaultTGAPIBase。
	TGProxy *url.URL
	AIProxy *url.URL

	// TurnstileSiteKey / TurnstileSecret 是网页申诉验证用的 Cloudflare
	// Turnstile 密钥。为空则验证页不可用（申诉降级为 noweb）。
	// 注意：Turnstile 后台的域名白名单必须包含 public_url 的主机名。
	TurnstileSiteKey string
	TurnstileSecret  string

	// TGAPIID / TGAPIHash 是客户端应用标识（my.telegram.org 申请），
	// 只用于一项可选能力：用 bot 自己的 token 登录 MTProto，回查历史成员的
	// 入群时间（Bot API 没有这个字段）。不配时走内置的公开 Telegram Desktop
	// 值（defaultTGAPIID/defaultTGAPIHash），无需配置即可用；想彻底关掉这一项
	// 时把 tg_api_id 显式配成空串。与 bot token 不同，它不代表任何账号的控制权。
	TGAPIID   int
	TGAPIHash string

	// ClientIPHeader 是取客户端真实 IP 的请求头，如 CF-Connecting-IP、
	// X-Real-IP。为空则取对端地址。
	//
	// 前提是 listen_addr 只绑回环、流量全部经过反代 —— 这个请求头才可信。
	ClientIPHeader string
}

const defaultListenAddr = "127.0.0.1:8081"

// defaultTGAPIID / defaultTGAPIHash 是内置的 MTProto 客户端标识：公开的
// Telegram Desktop 值，只用于入群时间回查（用 bot 的 token 登录 MTProto
// 读成员记录的 date）。没配就用它们，无需配置即可用 —— 想换成自己的
// （my.telegram.org 免费申请），在配置文件或环境变量里覆盖即可；想彻底
// 关掉这一项（不发起任何 MTProto 连接），把 tg_api_id 显式配成空串。
const (
	defaultTGAPIID   = 2040
	defaultTGAPIHash = "b18441a1ff607e10a989891a5462e627"
)

// UseWebhook 报告是否走 webhook 模式。
func (c *Config) UseWebhook() bool { return c.PublicURL != "" }

// WebhookURLFor 拼出某个 token 的完整回调地址：<public_url>/<token>/webhook。
func (c *Config) WebhookURLFor(token string) string {
	return strings.TrimRight(c.PublicURL, "/") + "/" + token + "/webhook"
}

func (c *Config) IsAdmin(id int64) bool {
	for _, a := range c.AdminIDs {
		if a == id {
			return true
		}
	}
	return false
}

// Load 读取并校验配置：先解析配置文件，再由环境变量覆盖。
//
// 手写解析该格式而不引入 YAML 依赖，支持 "key: value" 与 admin_ids 的
// "- N" / "[1, 2]" 写法。
func Load(path string) (*Config, error) {
	c := &Config{DBPath: "data.db", TGAPIBase: defaultTGAPIBase,
		ListenAddr: defaultListenAddr,
		TGAPIID:    defaultTGAPIID, TGAPIHash: defaultTGAPIHash}

	if err := loadFile(path, c); err != nil {
		return nil, err
	}
	// 环境变量在文件之后应用，因此压过文件。
	if err := applyEnv(c); err != nil {
		return nil, err
	}

	// 轮询是本进程主动发起的，没有 token 就无从发起；webhook 是 bot 主动
	// 推过来的，token 在路径里，配置里可以一个字都不写。
	if c.BotToken == "" && !c.UseWebhook() {
		return nil, fmt.Errorf("配置缺少 bot_token（长轮询模式必填；" +
			"或配置 public_url 改用 webhook 模式）")
	}
	if c.UseWebhook() && !strings.HasPrefix(c.PublicURL, "https://") {
		// TG 只接受 https 回调地址，配错在启动时就要拦下——
		// 否则表现为 setWebhook 静默失败、一条更新都收不到。
		return nil, fmt.Errorf("public_url 必须以 https:// 开头（Telegram 不接受 http）")
	}
	if len(c.AdminIDs) == 0 {
		return nil, fmt.Errorf("配置缺少 admin_ids：没有管理员就无人能配置本服务")
	}
	return c, nil
}

// loadFile 解析配置文件。文件不存在不算错误：容器部署常见做法是不挂载
// 配置、全靠环境变量，此时记一行日志并继续。其余打开失败（权限、是目录）
// 照常返回错误。
func loadFile(path string, c *Config) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		slog.Info("未找到配置文件，全部配置项从环境变量读取", "path", path)
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	inAdminList := false

	for sc.Scan() {
		trimmed := strings.TrimSpace(sc.Text())
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if inAdminList && strings.HasPrefix(trimmed, "- ") {
			if n, err := strconv.ParseInt(strings.TrimSpace(trimmed[2:]), 10, 64); err == nil {
				c.AdminIDs = append(c.AdminIDs, n)
			}
			continue
		}
		inAdminList = false

		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)

		// admin_ids 的多行写法要在这里起头，它跨行、进不了 assign。
		if key == "admin_ids" && val == "" {
			inAdminList = true
			continue
		}
		if err := assign(c, key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}

// envKeys 是环境变量到配置键的映射。
//
// 一律带 MENSHEN_ 前缀：避免占用 BOT_TOKEN 这类通用名字而与其他进程冲突，
// 也避免与 Go 的 ProxyFromEnvironment 读取的 HTTP_PROXY 混淆。
//
// 用切片而不是 map：map 遍历顺序随机，多项配错时报错项不稳定。
var envKeys = []struct{ env, key string }{
	{"MENSHEN_BOT_TOKEN", "bot_token"},
	{"MENSHEN_ADMIN_IDS", "admin_ids"},
	{"MENSHEN_TG_API_BASE", "tg_api_base"},
	{"MENSHEN_TG_PROXY", "tg_proxy"},
	{"MENSHEN_AI_PROXY", "ai_proxy"},
	{"MENSHEN_DB_PATH", "db_path"},
	{"MENSHEN_PUBLIC_URL", "public_url"},
	{"MENSHEN_LISTEN_ADDR", "listen_addr"},
	{"MENSHEN_TURNSTILE_SITE_KEY", "turnstile_site_key"},
	{"MENSHEN_TURNSTILE_SECRET", "turnstile_secret"},
	{"MENSHEN_CLIENT_IP_HEADER", "client_ip_header"},
	{"MENSHEN_TG_API_ID", "tg_api_id"},
	{"MENSHEN_TG_API_HASH", "tg_api_hash"},
}

// applyEnv 把环境变量盖到已有配置上。
//
// 用 LookupEnv 而不是 Getenv：要区分未设置与设为空串，后者表示显式清空。
func applyEnv(c *Config) error {
	for _, e := range envKeys {
		v, ok := os.LookupEnv(e.env)
		if !ok {
			continue
		}
		if err := assign(c, e.key, strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("%s: %w", e.env, err)
		}
	}
	return nil
}

// assign 把一个键值对写进 Config。
//
// 文件解析与环境变量共用它，两条路径的校验因此保持一致。新增配置项只需
// 在这里加一个分支，再往 envKeys 里补一行。
func assign(c *Config, key, val string) error {
	switch key {
	case "bot_token":
		c.BotToken = val
	case "tg_api_base":
		if val != "" {
			// 统一去掉尾部斜杠：拼接侧只补一个，两边都留会拼出双斜杠。
			c.TGAPIBase = strings.TrimRight(val, "/")
		}
	case "tg_proxy":
		u, err := parseProxy(val)
		if err != nil {
			return fmt.Errorf("tg_proxy: %w", err)
		}
		c.TGProxy = u
	case "ai_proxy":
		u, err := parseProxy(val)
		if err != nil {
			return fmt.Errorf("ai_proxy: %w", err)
		}
		c.AIProxy = u
	case "db_path":
		if val != "" {
			c.DBPath = val
		}
	case "public_url":
		c.PublicURL = strings.TrimRight(val, "/")
	case "listen_addr":
		if val != "" {
			c.ListenAddr = val
		}
	case "turnstile_site_key":
		c.TurnstileSiteKey = strings.TrimSpace(val)
	case "turnstile_secret":
		c.TurnstileSecret = strings.TrimSpace(val)
	case "tg_api_id":
		// 客户端应用标识（见 Config.TGAPIID）。不配走内置默认值；
		// 显式配 0 或空串 = 关闭入群时间回查。非法值不挡住启动，
		// 按关闭处理。
		if val == "" {
			c.TGAPIID = 0
			break
		}
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 0 {
			return fmt.Errorf("tg_api_id 应为非负整数（0 = 关闭入群时间回查），得到 %q", val)
		}
		c.TGAPIID = n
	case "tg_api_hash":
		c.TGAPIHash = strings.TrimSpace(val)
	case "admin_ids":
		// 接受三种形态：环境变量的 "1,2,3"、行内数组 "[1, 2]"、空格分隔。
		// 整体替换而不是追加：环境变量的语义是覆盖。
		c.AdminIDs = nil
		for _, part := range strings.FieldsFunc(strings.Trim(val, "[]"),
			func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return fmt.Errorf("admin_ids: %q 不是数字 user_id", part)
			}
			c.AdminIDs = append(c.AdminIDs, n)
		}
	default:
		// 未知键静默忽略难以排查：键名拼错时配置停留在默认值，而启动日志
		// 与面板上一切正常。此处记一行警告。
		slog.Warn("配置项未知，已忽略", "key", key)
	}
	return nil
}

// parseProxy 解析代理地址，空串返回 nil（交给环境变量）。
//
// 漏写协议前缀需要两道校验才能挡住：
//
//   - 地址以 IP 开头时 url.Parse 会自己报错，因为 scheme 不能以数字开头
//   - 主机名形式会静默通过：解析结果的 Scheme 是主机名、Host 为空、
//     Opaque 是端口，http.ProxyURL 既不报错也无法连接
//
// 所以在 url.Parse 的错误之外再加一道 scheme 白名单。两道校验都放在
// 启动时，不等运行时。
func parseProxy(v string) (*url.URL, error) {
	if v == "" {
		return nil, nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, fmt.Errorf("无法解析 %q: %w", v, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf(
			"%q 缺少协议前缀或协议不受支持，应形如 http://127.0.0.1:7890"+
				"（可用 http / https / socks5 / socks5h）", v)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q 缺少主机名", v)
	}
	return u, nil
}

// ProxyTransport 按代理地址建一个 RoundTripper，供 http.Client 使用。
//
// p 为 nil 时返回 nil，而不是一个 Proxy 为 nil 的 Transport：只有
// http.Client 的 Transport 字段为 nil 时才会落到 http.DefaultTransport，
// 而它的 Proxy 是 ProxyFromEnvironment，即留空时读环境变量。返回一个
// Proxy 为 nil 的 Transport 等于强制直连，会使环境变量失效。
//
// 用 Clone 而不是 &http.Transport{Proxy: …}：后者会丢掉 DefaultTransport
// 的连接池与各段超时（MaxIdleConns / IdleConnTimeout / TLS 握手超时），
// 表现为高频判定下连接不复用、句柄数持续增长。
func ProxyTransport(p *url.URL) http.RoundTripper {
	if p == nil {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(p)
	return tr
}

// tokenRe 是 Telegram bot token 的形态：<bot_id>:<随机串>。
//
// 校验它不是为了挑剔格式，而是为了守住 webhook 的入口：路径里的 token
// 会被用来建立 Bot 实例，不设门槛的话，任何人往 /bot<随便什么> 发一次
// POST 就能让本进程凭空多一个实例，刷够量即是内存耗尽。
// 格式过关之后还有一道 getMe 验真，见 core.ProbeBot。
var tokenRe = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{30,}$`)

func ValidToken(t string) bool { return tokenRe.MatchString(t) }

// MaskToken 供日志使用。token 等同于 bot 的完整控制权，
// 任何一条日志里都不得出现它的全文。
func MaskToken(t string) string {
	id, _, ok := strings.Cut(t, ":")
	if !ok {
		return "***"
	}
	return id + ":***"
}
