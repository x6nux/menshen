package antiad

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"menshen/internal/core"
)

// ---- 网页基础设施：签名密钥、链接签名、路由 ----
//
// 网页只在 webhook 模式（public_url 非空）下存在。链接形如
// <public_url>/_w/<用途>/<id>/<签名>；main 把路径中带 "_w" 段的请求
// 交给 WebHandler，其余照旧进 webhook。

// EnsureWebSecret 确保 settings.web_secret 存在（webhook 模式启动时调用）。
//
// 不进 settingDefaults：空值的含义是「还没生成」，给默认值会让所有部署
// 共用同一个密钥。在启动阶段生成而不是首次使用时：并发下两次生成会互相
// 覆盖，先签出去的链接随之失效。
func EnsureWebSecret(sh *core.Shared) error {
	if sh.Cache.Snap().Setting("web_secret") != "" {
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	if err := sh.PutSetting("web_secret", hex.EncodeToString(buf)); err != nil {
		return err
	}
	slog.Info("已生成网页签名密钥")
	return nil
}

// webSig 是网页链接的签名：hex(HMAC-SHA256(secret, msg))[:32]。
// 用途前缀写进 msg（ap: / apv: / v: / vp:），一处的签名拿不到另一处用。
func webSig(sh *core.Shared, msg string) string {
	secret := sh.Cache.Snap().Setting("web_secret")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// webSigOK 校验签名，常数时间比较。
func webSigOK(sh *core.Shared, msg, sig string) bool {
	want := webSig(sh, msg)
	return hmac.Equal([]byte(want), []byte(sig))
}

func appealSig(sh *core.Shared, id, uid int64) string {
	return webSig(sh, fmt.Sprintf("ap:%d:%d", id, uid))
}

func appealViewSig(sh *core.Shared, id int64) string {
	return webSig(sh, fmt.Sprintf("apv:%d", id))
}

func logViewSig(sh *core.Shared, id int64) string {
	return webSig(sh, fmt.Sprintf("v:%d", id))
}

func logViewPostSig(sh *core.Shared, id, exp int64) string {
	return webSig(sh, fmt.Sprintf("vp:%d:%d", id, exp))
}

// webURL 拼出对外链接；网页不可用（没配 public_url）时返回空串，
// 调用方按「网页不可用」处理。
func webURL(sh *core.Shared, path string) string {
	base := strings.TrimRight(sh.Cfg.PublicURL, "/")
	if base == "" {
		return ""
	}
	return base + "/_w/" + path
}

// AppealURL 是申诉人自己的验证页链接。
func AppealURL(sh *core.Shared, appealID, uid int64) string {
	return webURL(sh, fmt.Sprintf("ap/%d/%s", appealID, appealSig(sh, appealID, uid)))
}

// AppealDetailURL 是管理员看的申诉详情页链接。
func AppealDetailURL(sh *core.Shared, appealID int64) string {
	return webURL(sh, fmt.Sprintf("apv/%d/%s", appealID, appealViewSig(sh, appealID)))
}

// LogViewURL 是原文查看页链接。
func LogViewURL(sh *core.Shared, logID int64) string {
	return webURL(sh, fmt.Sprintf("v/%d/%s", logID, logViewSig(sh, logID)))
}

// WebAvailable 报告网页子系统是否可用（webhook 模式且有签名密钥）。
func WebAvailable(sh *core.Shared) bool {
	return sh.Cfg.PublicURL != "" && sh.Cache.Snap().Setting("web_secret") != ""
}

// ---- 路由 ----

type webRoute struct {
	kind string // ap / apv / v
	id   int64
	sig  string
}

// parseWebRoute 从请求路径里解析网页路由。
//
// 与 TokenFromPath 同一思路：不依赖前缀，路径中任一段为 "_w" 即命中，
// 反代再套几层子路径都认得出来。形状：.../_w/<kind>/<id>/<sig>
func parseWebRoute(p string) (webRoute, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		if s != "_w" {
			continue
		}
		if i+3 >= len(segs) {
			return webRoute{}, false
		}
		id, err := strconv.ParseInt(segs[i+2], 10, 64)
		if err != nil || id <= 0 {
			return webRoute{}, false
		}
		return webRoute{kind: segs[i+1], id: id, sig: segs[i+3]}, true
	}
	return webRoute{}, false
}

// WebHandler 处理 _w 下的网页请求。
//
// 签名不对、记录不存在一律 404，不区分两者 —— 免得被人按编号扫出
// 哪些申诉单存在。各页面的实现见 appeal_web.go / adview.go 的对应阶段。
func WebHandler(sh *core.Shared) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := parseWebRoute(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch route.kind {
		case "ap":
			handleAppealPage(sh, w, r, route)
		case "apv":
			handleAppealDetailPage(sh, w, r, route)
		case "v":
			handleLogViewPage(sh, w, r, route)
		default:
			http.NotFound(w, r)
		}
	})
}

// ---- 页面处理器（分阶段实现，先占位保证路由可用）----

func handleAppealPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	http.NotFound(w, r)
}

func handleAppealDetailPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	http.NotFound(w, r)
}

func handleLogViewPage(sh *core.Shared, w http.ResponseWriter, r *http.Request, rt webRoute) {
	http.NotFound(w, r)
}
