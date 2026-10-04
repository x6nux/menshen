package antiad

import (
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"menshen/internal/core"
)

// ---- 必封规则的维护（面板与规则发现共用）----
//
// 防误封由这里强制，不靠界面：保存先跑全库测试，enforce 只允许在
// 测过且 last_fp=0、last_undone=0 时打开，改了 pattern 就把 enforce 归零。
// 规则默认候选；enforce=1 命中即最高档处置（零 AI 成本），enforce=0
// 命中只作为证据进 prompt。

// RuleRow 是规则列表的一行（JSON 形状即 Mini App 的前端契约）。
type RuleRow struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	Pattern      string           `json:"pattern"`
	Category     string           `json:"category"`
	Note         string           `json:"note"`
	Source       string           `json:"source"`
	Enabled      bool             `json:"enabled"`
	Enforce      bool             `json:"enforce"`
	Hits         int64            `json:"hits"`
	LastMatched  int64            `json:"last_matched"`
	LastTP       int64            `json:"last_tp"`
	LastFP       int64            `json:"last_fp"`
	LastUndone   int64            `json:"last_undone"`
	LastScanned  int64            `json:"last_scanned"`
	LastTestedAt int64            `json:"last_tested_at"`
	LastAdsTotal int64            `json:"last_ads_total"`
	LastKinds    []map[string]any `json:"last_kinds"`
	CreatedAt    int64            `json:"created_at"`
}

// ListRules 返回全部规则（含未启用），按 id 升序。
func ListRules(sh *core.Shared) ([]RuleRow, error) {
	rows, err := sh.Store.Read.Query(`SELECT id,name,pattern,category,note,source,
		enabled,enforce,hits,last_matched,last_tp,last_fp,last_undone,
		last_scanned,last_tested_at,last_ads_total,last_kinds,
		created_at FROM ad_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleRow{}
	for rows.Next() {
		var r RuleRow
		var en, enf int64
		var kinds string
		if err := rows.Scan(&r.ID, &r.Name, &r.Pattern, &r.Category, &r.Note, &r.Source,
			&en, &enf, &r.Hits, &r.LastMatched, &r.LastTP, &r.LastFP, &r.LastUndone,
			&r.LastScanned, &r.LastTestedAt, &r.LastAdsTotal, &kinds, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled, r.Enforce = en == 1, enf == 1
		r.LastKinds = ParseRuleKindsJSON(kinds)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleInput 是一条规则可编辑的部分。
type RuleInput struct {
	Name, Pattern, Category, Note string
}

// validateRule 做严格校验：空串、超长、编译失败、能匹配空文本的规则都在
// 这里拒掉。试跑草稿（TestRulePattern）不要求过这一关。
func validateRule(in RuleInput) error {
	if n := len([]rune(in.Name)); n < 1 || n > 60 {
		return core.Bad("名称需为 1~60 个字符")
	}
	if _, err := CompileRulePattern(in.Pattern); err != nil {
		return core.Bad("%s", err.Error())
	}
	if len([]rune(in.Category)) > 20 {
		return core.Bad("分类最多 20 个字符")
	}
	if len([]rune(in.Note)) > 300 {
		return core.Bad("备注最多 300 个字符")
	}
	return nil
}

// checkRuleDup 拒绝重复正则：同一条正则在库里只允许存在一份，否则命中时
// 会产生两条流水，计数与处置也会重复评估。exceptID 是正在更新的那一条。
func checkRuleDup(sh *core.Shared, pattern string, exceptID int64) error {
	var n int64
	if err := sh.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM ad_rules WHERE pattern=? AND id<>?`,
		pattern, exceptID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return core.Bad("已存在相同正则的规则")
	}
	return nil
}

// SaveRule 新建（id=0）或更新一条规则：校验 → 写库 → 跑全库测试并写回
// last_* → 重建快照。返回规则 id 与这一轮的测试结果。
func SaveRule(sh *core.Shared, uid, id int64, in RuleInput) (int64, RuleTestResult, error) {
	created := id == 0
	if err := validateRule(in); err != nil {
		return 0, RuleTestResult{}, err
	}
	if err := checkRuleDup(sh, in.Pattern, id); err != nil {
		return 0, RuleTestResult{}, err
	}
	if created {
		res, err := sh.Store.Write.Exec(`INSERT INTO ad_rules
			(name,pattern,category,note,source,enabled,enforce,created_at,created_by)
			VALUES (?,?,?,?, 'ai', 0, 0, ?, ?)`,
			in.Name, in.Pattern, in.Category, in.Note, time.Now().Unix(), uid)
		if err != nil {
			return 0, RuleTestResult{}, err
		}
		id, _ = res.LastInsertId()
	} else {
		// 改了 pattern 就同时把 enforce 置 0：enforce 的前提是「这一版
		// pattern 的全库测试 fp=0」，新 pattern 的测试还没跑，旧结论不能
		// 沿用（否则改规则绕过防误封不变量）。只改 name/note 时保留 enforce。
		// SQLite 的 SET 表达式取旧行值，`pattern<>?` 比较的是库里的旧 pattern。
		res, err := sh.Store.Write.Exec(`UPDATE ad_rules
			SET name=?,pattern=?,category=?,note=?,
			    enforce=CASE WHEN pattern<>? THEN 0 ELSE enforce END
			WHERE id=?`,
			in.Name, in.Pattern, in.Category, in.Note, in.Pattern, id)
		if err != nil {
			return 0, RuleTestResult{}, err
		}
		// 规则存在但值完全没变时 RowsAffected 为 0；先查一次存在性再下结论。
		if n, _ := res.RowsAffected(); n == 0 && !ruleExists(sh, id) {
			return 0, RuleTestResult{}, core.Bad("规则不存在")
		}
	}

	// 保存即测：enforce 的前提数据必须是这一版 pattern 的真实结果，
	// 不能沿用改规则之前的那一轮。
	res, err := TestRulePattern(sh, in.Pattern)
	if err != nil {
		return 0, RuleTestResult{}, core.Bad("测试失败：%s", err.Error())
	}
	if err := writeRuleTest(sh, id, res); err != nil {
		return 0, RuleTestResult{}, err
	}
	// 新建且无误封：默认启用（命中只作为判定证据，强制仍需管理员手动开启）。
	// 更新已有规则时不动 enabled，避免保存备注把管理员停用的规则又打开。
	if created && res.FP == 0 && res.Undone == 0 {
		if _, err := sh.Store.Write.Exec(
			`UPDATE ad_rules SET enabled=1 WHERE id=?`, id); err != nil {
			return 0, RuleTestResult{}, err
		}
	}
	reloadRules(sh)
	return id, res, nil
}

// TestRuleByID 用库里那条规则跑一次全库测试，并把结果写回 last_*。
func TestRuleByID(sh *core.Shared, id int64) (RuleTestResult, error) {
	var pattern string
	if err := sh.Store.Read.QueryRow(
		`SELECT pattern FROM ad_rules WHERE id=?`, id).Scan(&pattern); err != nil {
		return RuleTestResult{}, core.Bad("规则不存在")
	}
	res, err := TestRulePattern(sh, pattern)
	if err != nil {
		return RuleTestResult{}, core.Bad("测试失败：%s", err.Error())
	}
	if err := writeRuleTest(sh, id, res); err != nil {
		return RuleTestResult{}, err
	}
	return res, nil
}

// SetRuleEnabled 启用/停用。停用时同时清掉 enforce：再次启用必须重新
// 通过全库测试，不能靠一个开关把最高档处置直接放回来。
func SetRuleEnabled(sh *core.Shared, id int64, on bool) error {
	q := `UPDATE ad_rules SET enabled=0,enforce=0 WHERE id=?`
	if on {
		q = `UPDATE ad_rules SET enabled=1 WHERE id=?`
	}
	return execRule(sh, id, q, id)
}

// SetRuleEnforce 开关强制。打开前必须 enabled=1、跑过全库测试
// （last_tested_at>0），且那一轮没有误封（last_fp=0 且 last_undone=0）；
// 关闭随时允许。
func SetRuleEnforce(sh *core.Shared, id int64, on bool) error {
	if on {
		var en, fp, undone, tested int64
		err := sh.Store.Read.QueryRow(
			`SELECT enabled,last_fp,last_undone,last_tested_at FROM ad_rules
			WHERE id=?`, id).Scan(&en, &fp, &undone, &tested)
		if errors.Is(err, sql.ErrNoRows) {
			return core.Bad("规则不存在")
		}
		if err != nil {
			return err
		}
		if en != 1 {
			return core.Bad("规则未启用，不能开启强制")
		}
		// 从未跑过全库测试（含 AI 直接写库的候选）不允许打开强制：
		// last_fp 的默认 0 只代表「没测出误封」，不代表「测过且干净」。
		if tested == 0 {
			return core.Bad("规则还没跑过全库测试，请先跑一次测试再开启强制")
		}
		if fp != 0 || undone != 0 {
			// undone 已计入 fp；db 被手改时单独兜一下，避免文案报「0 条」。
			return core.Bad("规则尚未通过全库测试（疑似误封 %d 条），不能开启强制",
				max(fp, undone))
		}
	}
	enforce := 0
	if on {
		enforce = 1
	}
	return execRule(sh, id, `UPDATE ad_rules SET enforce=? WHERE id=?`, enforce, id)
}

// DeleteRule 删除一条规则。
func DeleteRule(sh *core.Shared, id int64) error {
	return execRule(sh, id, `DELETE FROM ad_rules WHERE id=?`, id)
}

// execRule 执行一条针对单条规则的写操作；一行都没碰到且规则确实不存在时
// 报「规则不存在」。成功后重建快照，判定路径立即生效。
func execRule(sh *core.Shared, id int64, q string, args ...any) error {
	if id == 0 {
		return core.Bad("缺少规则 id")
	}
	res, err := sh.Store.Write.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 && !ruleExists(sh, id) {
		return core.Bad("规则不存在")
	}
	reloadRules(sh)
	return nil
}

func ruleExists(sh *core.Shared, id int64) bool {
	var one int64
	return sh.Store.Read.QueryRow(
		`SELECT 1 FROM ad_rules WHERE id=?`, id).Scan(&one) == nil
}

// writeRuleTest 把一轮全库测试的统计写回规则行。
// last_matched（最近命中时刻）不在这里动：它由判定路径的 BumpRuleHits 维护。
func writeRuleTest(sh *core.Shared, id int64, r RuleTestResult) error {
	_, err := sh.Store.Write.Exec(`UPDATE ad_rules
		SET last_tested_at=?,last_scanned=?,last_tp=?,last_fp=?,last_undone=?,
		    last_ads_total=?,last_kinds=?
		WHERE id=?`, time.Now().Unix(), r.Scanned, r.TP, r.FP, r.Undone,
		r.AdsTotal, RuleKindsJSON(r.Kinds), id)
	return err
}

func reloadRules(sh *core.Shared) {
	if err := sh.Cache.Reload(); err != nil {
		slog.Error("必封规则写库后刷新缓存失败", "err", err)
	}
}
