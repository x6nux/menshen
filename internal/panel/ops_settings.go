package panel

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
)

// ---- 设置项的写入规则（TG 面板与 Mini App 共用）----
//
// 权限、取值校验、「-」的含义、清旧键都只在这里写一次。两个界面曾经各写
// 一份，结果识图模型填「-」一边清空一边报错、布尔开关一边放行两个一边
// 放行四个 —— 同一个设置项在不同入口有不同规则，是最难排查的那种问题。

// globalToggles 是一键切换的全局 0/1 开关（不在 settingSpecs 里）。
var globalToggles = []string{"antiad_enabled", "alert_copy_main", "gban_enabled", "antiad_rule_auto"}

// singleModelKeys 是单值模型设置；列表型的见 legacyModelKey。
var singleModelKeys = []string{"antiad_vision_model", "antiad_rule_model"}

// setSetting 写一项设置。botID 为 0 写全局值（只有主管理员能写）；否则写
// 该 bot 的覆盖值（只有 antiad / both 组的整数项能覆盖），val 为空或「-」
// 表示撤销覆盖、跟随全局。
func setSetting(sh *core.Shared, uid, botID int64, key, val string) error {
	val = strings.TrimSpace(val)
	if botID != 0 {
		return setBotSetting(sh, uid, botID, key, val)
	}
	if !sh.IsMain(uid) {
		return core.Denied("只有主管理员能改全局设置")
	}
	reset := val == "" || val == "-"

	switch {
	case slices.Contains(globalToggles, key):
		if val != "0" && val != "1" {
			return core.Bad("取值必须是 0 或 1")
		}

	case key == tzSpec.key:
		if reset {
			val = "Asia/Shanghai"
		}
		if _, err := time.LoadLocation(val); err != nil {
			return core.Bad("不是有效的 IANA 时区名（如 Asia/Shanghai、Europe/London）")
		}

	case key == groupFooterSpec.key:
		// 自由文本，转义留到渲染时做。
		if val == "-" {
			val = ""
		}
		if len([]rune(val)) > 300 {
			return core.Bad("附加文本过长（上限 300 字）")
		}

	case slices.Contains(singleModelKeys, key):
		if reset {
			val = ""
		} else if err := modelUsable(sh, val); err != nil {
			return err
		}

	case legacyModelKey(key) != "":
		models := []string{}
		if !reset {
			var err error
			if models, err = parseModelList(sh.Cache.Snap(), val); err != nil {
				return core.Bad("%s", err.Error())
			}
		}
		if err := sh.PutSetting(key, modelsJSON(models)); err != nil {
			return err
		}
		// 旧单值键也要清：留着的话列表被清空后读侧会回退到它，出现
		// 「面板显示空、实际还跑着旧模型」的错位。
		return sh.PutSetting(legacyModelKey(key), "")

	default:
		// 主管理员也能给 antiad 组设**全局默认**（各 bot 不覆盖时用它）。
		sp := settingSpecByKey(key)
		if sp == nil {
			return core.Bad("未知设置项")
		}
		if err := checkSpecValue(sp, val); err != nil {
			return err
		}
	}
	return sh.PutSetting(key, val)
}

func setBotSetting(sh *core.Shared, uid, botID int64, key, val string) error {
	sp := settingSpecByKey(key)
	if sp == nil {
		return core.Bad("未知设置项")
	}
	if sp.group != "antiad" && sp.group != "both" {
		return core.Bad("该项不能按 bot 覆盖")
	}
	if !sh.CanManageBot(uid, botID) {
		return core.Denied("无权管理该 bot")
	}
	if val == "" || val == "-" {
		// 写成与全局相同的值 = 删除覆盖（见 PutBotSetting）。
		return sh.PutBotSetting(botID, key, sh.Cache.Snap().Setting(key))
	}
	if err := checkSpecValue(sp, val); err != nil {
		return err
	}
	return sh.PutBotSetting(botID, key, val)
}

// checkSpecValue 校验整数型设置项的取值（闭区间，max 为 0 表示无上限）。
func checkSpecValue(sp *settingSpec, val string) error {
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil || n < sp.min || (sp.max != 0 && n > sp.max) {
		return core.Bad("取值非法（%s）", sp.hint)
	}
	return nil
}

// modelUsable 校验一个模型名存在且启用。
//
// 必须校验：配了不存在的模型名，链路会在每条消息上向上游拿回 404，而
// 404 在 bot 侧只表现为「判定失败 → 放行」，功能静默失效。
func modelUsable(sh *core.Shared, name string) error {
	m := sh.Cache.Snap().Models[name]
	if m == nil {
		return core.Bad("模型不存在，请在「模型定价」里确认名称")
	}
	if !m.Enabled {
		return core.Bad("该模型当前是禁用状态，请先启用它，或换一个")
	}
	return nil
}

// setBotModels 设置某个 bot 专用的判定 / 复判模型列表（which = so / llm），
// 空或「-」= 沿用全局默认。模型直接决定判定质量与花掉多少钱，只有主管理员能配。
func setBotModels(sh *core.Shared, uid, botID int64, which, text string) ([]string, error) {
	if !sh.IsMain(uid) {
		return nil, core.Denied("模型由主管理员配置")
	}
	if which != "so" && which != "llm" {
		return nil, core.Bad("which 必须是 so 或 llm")
	}
	var models []string
	if text = strings.TrimSpace(text); text != "" && text != "-" {
		var err error
		if models, err = parseModelList(sh.Cache.Snap(), text); err != nil {
			return nil, core.Bad("%s", err.Error())
		}
	}
	return models, sh.SetBotModels(botID, which, models)
}

// setDigest 改形态摘要（fix=false）或给总结模型的修正文本（fix=true）。
// 「-」= 清空；摘要按 antiad_digest_max 截断 —— 它会注入此后每一条判定。
func setDigest(sh *core.Shared, uid int64, fix bool, text string) error {
	if !sh.IsMain(uid) {
		return core.Denied("形态摘要由主管理员维护")
	}
	v := strings.TrimSpace(text)
	if v == "-" {
		v = ""
	}
	if fix {
		return sh.PutSetting("antiad_digest_fix", core.TruncateRunes(v, antiad.DigestFixLimit))
	}
	limit := int(sh.Cache.Snap().SettingInt("antiad_digest_max", 1200))
	return sh.PutSetting("antiad_digest", core.TruncateRunes(v, limit))
}
