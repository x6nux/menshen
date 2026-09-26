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
// 默认走反代而不是官方地址：直连 api.telegram.org 在不少网络环境下
// 连 TCP 都握不上手（dial tcp 149.154.x.x:443: i/o timeout），
// 而那是整个 bot 唯一的输入通道，不通就等于服务没启动。
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
	// 这个端口上的请求等同于「以 bot 身份收发消息」，直接暴露到公网
	// 等于把 bot 的控制面摆在门口。
	ListenAddr string

	// TGProxy / AIProxy 是两类出网请求各自的代理，nil 表示不指定 ——
	// 此时沿用 HTTP_PROXY / HTTPS_PROXY 环境变量（Go 的默认行为）。
	// 留空而不是直连，是为了不让现在靠环境变量跑着的部署升级后突然断网。
	//
	// 分开配而不是共用一个：最常见的部署形态是「TG 被墙、AI 走国内中转」，
	// 把 AI 流量也绕一圈只是白多一跳。两者都留空则两者都读环境变量。
	//
	// 代理**不是**换 API 地址的替代品：tg_api_base 换的是目标地址，
	// 代理换的是到达路径。反代通常比代理更省事，见 defaultTGAPIBase。
	TGProxy *url.URL
	AIProxy *url.URL
}

const defaultListenAddr = "127.0.0.1:8081"

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

// ponytail: 手写解析这个只有四个字段的格式，不引入 YAML 依赖。
// 支持 "key: value" 和 admin_ids 的 "- N" / "[1, 2]" 两种写法。
func Load(path string) (*Config, error) {
	c := &Config{DBPath: "data.db", TGAPIBase: defaultTGAPIBase,
		ListenAddr: defaultListenAddr}

	if err := loadFile(path, c); err != nil {
		return nil, err
	}
	// 环境变量后跑，因此压过文件。容器里最常见的形态是「镜像里烤了一份
	// config.yaml，用环境变量覆盖个别项」；反过来的话环境变量看着配了
	// 却不生效，而这个问题只在容器里出现，本地怎么跑都复现不了。
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

// loadFile 解析配置文件。文件不存在**不算错误**：容器里常见的做法是完全
// 不挂载配置、全靠环境变量，为此强迫塞一个空文件很蠢。但会记一行日志 ——
// 否则把路径写错会表现成「所有配置都没生效」，而那很难往这里想。
//
// 其余的打开失败（权限、是目录）照常返回错误：那些是真出了问题。
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
// 一律带 MENSHEN_ 前缀：不占用 BOT_TOKEN 这种通用名字，免得跟同一个容器
// 里别的进程撞车；更要紧的是 HTTP_PROXY 已经被 Go 的 ProxyFromEnvironment
// 用着了，不加前缀的 TG_PROXY 会让人以为是同一套东西。
//
// 用切片而不是 map：map 的遍历顺序随机，配错两项时每次报的是哪一项都不
// 一样，排障时会以为自己改错了地方。
var envKeys = []struct{ env, key string }{
	{"MENSHEN_BOT_TOKEN", "bot_token"},
	{"MENSHEN_ADMIN_IDS", "admin_ids"},
	{"MENSHEN_TG_API_BASE", "tg_api_base"},
	{"MENSHEN_TG_PROXY", "tg_proxy"},
	{"MENSHEN_AI_PROXY", "ai_proxy"},
	{"MENSHEN_DB_PATH", "db_path"},
	{"MENSHEN_PUBLIC_URL", "public_url"},
	{"MENSHEN_LISTEN_ADDR", "listen_addr"},
}

// applyEnv 把环境变量盖到已有配置上。
//
// 用 LookupEnv 而不是 Getenv：要区分「没设」与「设成了空」。后者是显式
// 清空（例如镜像里的 config.yaml 配了代理，某个部署要关掉它）。
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

// assign 把一个「键 = 值」写进 Config。
//
// 文件解析与环境变量共用它，两条路径因此不会在校验上漂移 —— 容器里配错
// 代理与在文件里配错，得到的是同一条错误。新增配置项只需要在这里加一个
// 分支，再往 envKeys 里补一行。
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
	case "admin_ids":
		// 吃三种形态：环境变量的 "1,2,3"、行内数组 "[1, 2]"、空格分隔。
		// 整体替换而不是追加：环境变量的语义是覆盖，追加会让镜像里烤的
		// 那份名单永远去不掉。
		c.AdminIDs = nil
		for _, part := range strings.FieldsFunc(strings.Trim(val, "[]"),
			func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return fmt.Errorf("admin_ids: %q 不是数字 user_id", part)
			}
			c.AdminIDs = append(c.AdminIDs, n)
		}
	}
	return nil
}

// parseProxy 解析代理地址，空串返回 nil（交给环境变量）。
//
// 漏写协议前缀是这个配置项最容易踩的坑，而且要两道才挡得住：
//
//   - IP 形式的 url.Parse 会自己报错（"127.0.0.1:7890" → first path
//     segment in URL cannot contain colon），因为 scheme 不能以数字开头
//   - **主机名形式的会静默通过**："localhost:1080" 解析出来 Scheme 是
//     "localhost"、Host 为空、Opaque 是 "1080"。http.ProxyURL 拿着这么个
//     东西既连不上也不吭声
//
// 所以 err 之外还要一道 scheme 白名单。少了它，表现是所有出网请求超时，
// 而配置文件看着完全正常。两道都放在启动时，不等运行时。
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
// p 为 nil 时返回 nil，而不是「Proxy 为 nil 的 Transport」：两者差别很大。
// http.Client 的 Transport 字段为 nil 才会落到 http.DefaultTransport，
// 而后者的 Proxy 正是 ProxyFromEnvironment —— 这才是「留空 = 读环境变量」。
// 返回一个 Proxy 为 nil 的 Transport 等于强制直连，会把环境变量废掉。
//
// 用 Clone 而不是 &http.Transport{Proxy: …}：后者会丢掉 DefaultTransport
// 的连接池与各段超时（MaxIdleConns / IdleConnTimeout / TLS 握手超时），
// 表现是高频判定下连接不复用、句柄数一路涨，而功能看起来一切正常。
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
// 格式过关之后还有一道 getMe 验真，见 botRegistry.botFor。
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
