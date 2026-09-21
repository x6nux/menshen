package config

import (
	"bufio"
	"fmt"
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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := &Config{DBPath: "data.db", TGAPIBase: defaultTGAPIBase,
		ListenAddr: defaultListenAddr}
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

		switch key {
		case "bot_token":
			c.BotToken = val
		case "tg_api_base":
			if val != "" {
				// 统一去掉尾部斜杠：拼接侧只补一个，两边都留会拼出双斜杠。
				c.TGAPIBase = strings.TrimRight(val, "/")
			}
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
			if val == "" {
				inAdminList = true
			} else { // 行内数组 [1, 2]
				for _, part := range strings.Split(strings.Trim(val, "[]"), ",") {
					if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
						c.AdminIDs = append(c.AdminIDs, n)
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
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
