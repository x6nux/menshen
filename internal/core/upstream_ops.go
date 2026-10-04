package core

import (
	"sort"
	"strings"

	"menshen/internal/store"
	"menshen/internal/upstream"
)

// ---- 上游渠道的增删改 ----
//
// TG 面板与 Mini App 共用：base_url 的协议头、尾部斜杠、权重下限、按类型
// 收敛的能力开关都只在这里判一次。

// UpstreamPatch 是上游的部分更新：nil 字段不动。AddUpstream 也用它，
// 此时 Name / BaseURL 必填，其余缺省为权重 1、启用、openai、按类型的默认能力。
type UpstreamPatch struct {
	Name, BaseURL, APIKey *string
	Weight                *int64
	Enabled               *bool
	Kind                  *upstream.Kind
	Chat, SystemOne       *bool
}

// CleanBaseURL 校验协议头并去掉尾部斜杠（拼路径时会再补一个）。
func CleanBaseURL(u string) (string, error) {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", Bad("base_url 必须以 http:// 或 https:// 开头")
	}
	return strings.TrimSuffix(u, "/"), nil
}

// checkWeight 拒绝非正权重：总权重为 0 时加权选择退化成除零。
func checkWeight(w *int64) error {
	if w != nil && *w <= 0 {
		return Bad("权重必须是正整数")
	}
	return nil
}

func (sh *Shared) AddUpstream(p UpstreamPatch) error {
	if p.Name == nil || p.BaseURL == nil {
		return Bad("名称与 base_url 必填")
	}
	name := strings.TrimSpace(*p.Name)
	if err := ValidUpstreamName(name); err != nil {
		return Bad("%s", err.Error())
	}
	for _, u := range sh.Cache.Snap().Upstreams {
		if u.Name == name {
			return Bad("已存在同名上游")
		}
	}
	base, err := CleanBaseURL(*p.BaseURL)
	if err != nil {
		return err
	}
	if err := checkWeight(p.Weight); err != nil {
		return err
	}
	weight, enabled, kind := int64(1), true, upstream.KindOpenAI
	if p.Weight != nil {
		weight = *p.Weight
	}
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	if p.Kind != nil {
		kind = *p.Kind
	}
	key := ""
	if p.APIKey != nil {
		key = *p.APIKey
	}
	chat, so := kind.ResolveCaps(deref(p.Chat), deref(p.SystemOne),
		p.Chat != nil || p.SystemOne != nil)
	if _, err := sh.Store.Write.Exec(`INSERT INTO upstreams
		(name,base_url,api_key,weight,status,supports_chat,supports_systemone,kind)
		VALUES (?,?,?,?,?,?,?,?)`, name, base, key, weight, boolInt(enabled),
		boolInt(chat), boolInt(so), string(kind)); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// UpdateUpstream 按补丁改一个上游。改名走 RenameUpstream（连带改写模型名）。
//
// 能力开关：**真的换了类型**才按新类型重置（前端每次保存都会回传当前
// kind，不能据此覆盖用户的开关）；chat-only 类型无论传什么都只开 chat；
// 其余情况按传入的开关走。
func (sh *Shared) UpdateUpstream(id int64, p UpstreamPatch) error {
	cur := upstreamByID(sh.Cache.Snap(), id)
	if cur == nil {
		return Bad("上游不存在")
	}
	if err := checkWeight(p.Weight); err != nil {
		return err
	}
	var cols []string
	var args []any
	set := func(col string, v any) {
		cols = append(cols, col+"=?")
		args = append(args, v)
	}
	if p.BaseURL != nil {
		base, err := CleanBaseURL(*p.BaseURL)
		if err != nil {
			return err
		}
		set("base_url", base)
	}
	if p.APIKey != nil {
		if *p.APIKey == "" {
			return Bad("api_key 不能为空")
		}
		set("api_key", *p.APIKey)
	}
	if p.Weight != nil {
		set("weight", *p.Weight)
	}
	if p.Enabled != nil {
		set("status", boolInt(*p.Enabled))
	}
	kind := cur.EffectiveKind()
	kindChanged := p.Kind != nil && *p.Kind != kind
	if kindChanged {
		kind = *p.Kind
		set("kind", string(kind))
	}
	chat, so := cur.SupportsChat, cur.SupportsSystemOne
	if p.Chat != nil {
		chat = *p.Chat
	}
	if p.SystemOne != nil {
		so = *p.SystemOne
	}
	if kindChanged || kind.ChatOnly() || p.Chat != nil || p.SystemOne != nil {
		chat, so = kind.ResolveCaps(chat, so, !kindChanged)
		set("supports_chat", boolInt(chat))
		set("supports_systemone", boolInt(so))
	}

	if p.Name != nil {
		if err := sh.RenameUpstream(id, strings.TrimSpace(*p.Name)); err != nil {
			return Bad("%s", err.Error())
		}
	}
	if len(cols) == 0 {
		return nil
	}
	// cols 只来自上面的字面量，不存在注入面。
	if _, err := sh.Store.Write.Exec(`UPDATE upstreams SET `+strings.Join(cols, ",")+
		` WHERE id=?`, append(args, id)...); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// DeleteUpstream 删除上游。名下有模型时拒绝：模型名里嵌着上游名，删掉
// 上游会留下一批「绑定的上游不存在」的死引用 —— 判定每次都失败，而
// 面板上看不出原因。
func (sh *Shared) DeleteUpstream(id int64) error {
	if names := ModelsOfUpstream(sh.Cache.Snap(), id); len(names) > 0 {
		return Bad("该上游名下有模型，请先删除它们：%s", strings.Join(names, "、"))
	}
	if _, err := sh.Store.Write.Exec(`DELETE FROM upstreams WHERE id=?`, id); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// ModelsOfUpstream 返回绑在这个上游名下的模型名（全名，稳定排序）。
func ModelsOfUpstream(snap *store.Snapshot, id int64) []string {
	u := upstreamByID(snap, id)
	if u == nil {
		return nil
	}
	var out []string
	for full := range snap.Models {
		if up, _ := upstream.SplitModelName(full); up == u.Name {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out
}

func upstreamByID(snap *store.Snapshot, id int64) *upstream.Upstream {
	for _, u := range snap.Upstreams {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func deref(b *bool) bool { return b != nil && *b }
