package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"menshen/internal/store"
	"menshen/internal/upstream"
)

// ValidUpstreamName 校验上游名能否作为模型名前缀。
//
// 前缀按第一个 "/" 解析，所以上游名不能含 "/"；含 ":" 会让面板的
// callback_data 形态变脆，也一并拒绝。长度限制只是防呆。
func ValidUpstreamName(name string) error {
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if strings.ContainsAny(name, "/:") {
		return fmt.Errorf("名称不能包含 / 或 :（/ 是模型名的分隔符）")
	}
	if len(name) > 32 {
		return fmt.Errorf("名称过长（最多 32 字节）")
	}
	return nil
}

// RenameUpstream 给上游改名，并连带改写所有引用它的模型名与模型设置。
//
// 模型名 <上游名>/<模型ID> 里嵌着上游名：改名不改写引用的话，一批模型
// 会在一瞬间变成「绑定的上游不存在」——判定链路照跑、只是每次都失败，
// 而面板上一切正常。
func (sh *Shared) RenameUpstream(id int64, newName string) error {
	if err := ValidUpstreamName(newName); err != nil {
		return err
	}
	snap := sh.Cache.Snap()
	var old string
	for _, u := range snap.Upstreams {
		if u.ID == id {
			old = u.Name
			break
		}
	}
	if old == "" {
		return fmt.Errorf("上游不存在")
	}
	if old == newName {
		return nil
	}
	for _, u := range snap.Upstreams {
		if u.ID != id && u.Name == newName {
			return fmt.Errorf("已存在同名上游")
		}
	}

	tx, err := sh.Store.Write.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE upstreams SET name=? WHERE id=?`, newName, id); err != nil {
		return err
	}
	// models.name = old + "/" + 剩余部分 → new + "/" + 剩余部分。
	// 用 substr 而不是 LIKE：上游名里可能有 % 或 _，那在 LIKE 里是通配符。
	if _, err := tx.Exec(`UPDATE models SET name=? || substr(name, ?)
		WHERE substr(name, 1, ?)=?`,
		newName+"/", len(old)+2, len(old)+1, old+"/"); err != nil {
		return err
	}
	if err := renameModelRefs(tx, old, newName); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// renameModelName 把模型名里的 old 前缀改成 new；不属于该上游的原样返回。
func renameModelName(name, old, newName string) string {
	up, id := upstream.SplitModelName(name)
	if up == old {
		return newName + "/" + id
	}
	return name
}

// renameModelRefs 改写 settings 与 bots 里的模型引用。
// 只动确实以 old 为前缀的项，其余原样保留。
func renameModelRefs(tx *sql.Tx, old, newName string) error {
	for _, k := range []string{"antiad_so_models", "antiad_llm_models"} {
		v, err := settingValue(tx, k)
		if err != nil {
			return err
		}
		if v == "" {
			continue
		}
		models := store.ParseStringList(v)
		changed := false
		for i, m := range models {
			if nm := renameModelName(m, old, newName); nm != m {
				models[i], changed = nm, true
			}
		}
		if !changed {
			continue
		}
		raw, err := json.Marshal(models)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE settings SET v=? WHERE k=?`, string(raw), k); err != nil {
			return err
		}
	}
	for _, k := range []string{"antiad_so_model", "antiad_llm_model", "antiad_vision_model"} {
		v, err := settingValue(tx, k)
		if err != nil {
			return err
		}
		if nm := renameModelName(v, old, newName); nm != v {
			if _, err := tx.Exec(`UPDATE settings SET v=? WHERE k=?`, nm, k); err != nil {
				return err
			}
		}
	}

	rows, err := tx.Query(`SELECT bot_id,so_models,llm_models,so_model,llm_model FROM bots`)
	if err != nil {
		return err
	}
	type botRefs struct {
		id                  int64
		soModels, llmModels string
		soModel, llmModel   string
	}
	var refs []botRefs
	for rows.Next() {
		var r botRefs
		if err := rows.Scan(&r.id, &r.soModels, &r.llmModels, &r.soModel, &r.llmModel); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range refs {
		so, soChanged := renameModelList(r.soModels, old, newName)
		llm, llmChanged := renameModelList(r.llmModels, old, newName)
		soSingle := renameModelName(r.soModel, old, newName)
		llmSingle := renameModelName(r.llmModel, old, newName)
		if !soChanged && !llmChanged && soSingle == r.soModel && llmSingle == r.llmModel {
			continue
		}
		if _, err := tx.Exec(`UPDATE bots SET so_models=?,llm_models=?,so_model=?,llm_model=?
			WHERE bot_id=?`, so, llm, soSingle, llmSingle, r.id); err != nil {
			return err
		}
	}
	return nil
}

// renameModelList 改写一个 JSON 数组列；返回新值与是否变过。
func renameModelList(v, old, newName string) (string, bool) {
	models := store.ParseStringList(v)
	if len(models) == 0 {
		return v, false
	}
	changed := false
	for i, m := range models {
		if nm := renameModelName(m, old, newName); nm != m {
			models[i], changed = nm, true
		}
	}
	if !changed {
		return v, false
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return v, false
	}
	return string(raw), true
}

// settingValue 读一个设置项；不存在返回空串而不是错误。
func settingValue(tx *sql.Tx, k string) (string, error) {
	var v string
	err := tx.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}
