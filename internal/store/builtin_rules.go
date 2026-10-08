package store

import (
	"time"
)

// builtinRule 是一条随二进制分发的内置必封规则。与 AI 规则发现产出的规则
// 走同一张 ad_rules 表、同一套匹配与处置路径，区别只在来源：内置规则不依赖
// 模型总结，启动时按需补种，专门覆盖已被实测漏判的高精度形态。
type builtinRule struct {
	Name     string
	Pattern  string
	Category string
	Note     string
}

// builtinRules 是内置规则表。
//
// 目前只有一条：返利拉新。这类文案的核心是「邀请码 + 邀请链接」——邀请码
// 每次发布都随机（邀请码：M8P5J1），精确内容哈希的去重因此完全失效，而
// 初判模型对它的置信度又常常低到下限线以下。三种特征（自家邀请码、奖励
// 兑现、引流链接）里固定要求「邀请码后紧跟一串码」，纯讨论邀请码（如
// 「发一下你们的邀请码」）与单纯贴链接都不命中。
//
// enforce 取 0：命中先删 + 临时禁言，再交大模型复判定案，复判正常会自动
// 解除。这类形态毕竟只是模式匹配、不看语境，不适合不经 AI 直接最高档处置。
var builtinRules = []builtinRule{{
	Name: "返利拉新：自家邀请码+奖励/邀请链接（promo）",
	Pattern: `(?is)(邀請碼|邀请码)[：:\s]{0,4}[A-Za-z0-9_-]{5,}.{0,60}(https?://|t\.me/)` +
		`|(https?://|t\.me/)\S{0,40}.{0,20}(邀請碼|邀请码)[：:\s]{0,4}[A-Za-z0-9_-]{5,}` +
		`|(分别获得|分別獲得|双方各得|雙方各得|各获得|各自获得|各自獲得|各自領取|各自领取).{0,40}(邀請碼|邀请码)[：:\s]{0,4}[A-Za-z0-9_-]{5,}` +
		`|(邀請碼|邀请码)[：:\s]{0,4}[A-Za-z0-9_-]{5,}.{0,40}(词元|代幣|代币|积分|獎勵|奖励|返利|佣金|空投)`,
	Category: "promo",
	Note:     "邀请码与奖励承诺/邀请链接同现的返利拉新；纯讨论邀请码（无码无链接）不命中",
}}

// SeedBuiltinRules 补种缺失的内置规则，供服务启动时在构建缓存之前调用。
//
// 放在启动路径而不是 Open 里：内置规则是给线上判定用的，而 Open 也是所有
// 单测打开临时库的入口，在 Open 里补种会让每个用例的 ad_rules 都凭空多出
// 一条，规则相关的计数断言全部要跟着改口径。
//
// 按 source+name 判重：管理员停用或改写内置规则不会被下一次启动覆盖，
// 只有整行不存在时才插入。
func SeedBuiltinRules(st *Store) error {
	for _, r := range builtinRules {
		var n int
		if err := st.Read.QueryRow(
			`SELECT COUNT(*) FROM ad_rules WHERE source='builtin' AND name=?`,
			r.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := st.Write.Exec(`INSERT INTO ad_rules
			(name,pattern,category,note,source,enabled,enforce,created_at,created_by)
			VALUES (?,?,?,?,'builtin',1,0,?,0)`,
			r.Name, r.Pattern, r.Category, r.Note, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}
