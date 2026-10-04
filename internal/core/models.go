package core

import (
	"math"
	"strings"

	"menshen/internal/store"
)

// ---- 模型定价的增删改 ----
//
// TG 面板与 Mini App 共用。价格只用于把判定开销折算成钱，判定本身不依赖它。

// ModelPriceCols 是四个单价列（$ / 1M tokens），顺序即各处价格数组的顺序：
// 输入、补全、缓存读取、缓存创建。
var ModelPriceCols = [4]string{
	"prompt_price", "completion_price", "cache_read_price", "cache_write_price"}

// ModelNameMax 是模型全名的字节上限：TG 的 callback_data 只有 64 字节，
// 最长的形态是 "a:md:e:crp:" + 模型名，超了模型页整页发不出去。
const ModelNameMax = 64 - len("a:md:e:crp:")

// NewModelName 校验并拼出新模型的全名 <上游名>/<模型ID>。
func NewModelName(snap *store.Snapshot, upName, modelID string) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", Bad("模型 ID 不能为空")
	}
	if err := ValidUpstreamName(upName); err != nil {
		return "", Bad("%s", err.Error())
	}
	found := false
	for _, u := range snap.Upstreams {
		if u.Name == upName {
			found = true
			break
		}
	}
	if !found {
		return "", Bad("上游 %s 不存在", upName)
	}
	name := upName + "/" + modelID
	if len(name) > ModelNameMax {
		return "", Bad("模型全名过长（%d 字节），含上游前缀不得超过 %d 字节",
			len(name), ModelNameMax)
	}
	if snap.Models[name] != nil {
		return "", Bad("该模型已存在")
	}
	return name, nil
}

// CheckPrice 拒绝负价与非有限值：负价会让开销核算变成负数，NaN 会让面板
// 上的数字直接变成 NaN。
func CheckPrice(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return Bad("价格必须是非负数")
	}
	return nil
}

// AddModel 新增并启用一个模型，返回它的全名。
func (sh *Shared) AddModel(upName, modelID string, prices [4]float64) (string, error) {
	name, err := NewModelName(sh.Cache.Snap(), upName, modelID)
	if err != nil {
		return "", err
	}
	for _, p := range prices {
		if err := CheckPrice(p); err != nil {
			return "", err
		}
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO models
		(name,prompt_price,completion_price,cache_read_price,cache_write_price,enabled)
		VALUES (?,?,?,?,?,1)`, name, prices[0], prices[1], prices[2], prices[3]); err != nil {
		return "", err
	}
	return name, sh.Cache.Reload()
}

// ModelPatch 是模型的部分更新：nil 字段不动。Prices 的顺序同 ModelPriceCols。
type ModelPatch struct {
	Prices  [4]*float64
	Enabled *bool
}

func (sh *Shared) UpdateModel(name string, p ModelPatch) error {
	if sh.Cache.Snap().Models[name] == nil {
		return Bad("模型不存在")
	}
	var cols []string
	var args []any
	for i, v := range p.Prices {
		if v == nil {
			continue
		}
		if err := CheckPrice(*v); err != nil {
			return err
		}
		cols = append(cols, ModelPriceCols[i]+"=?")
		args = append(args, *v)
	}
	if p.Enabled != nil {
		cols = append(cols, "enabled=?")
		args = append(args, boolInt(*p.Enabled))
	}
	if len(cols) == 0 {
		return nil
	}
	// cols 只来自 ModelPriceCols 与字面量，不存在注入面。
	if _, err := sh.Store.Write.Exec(`UPDATE models SET `+strings.Join(cols, ",")+
		` WHERE name=?`, append(args, name)...); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

func (sh *Shared) DeleteModel(name string) error {
	if sh.Cache.Snap().Models[name] == nil {
		return Bad("模型不存在")
	}
	if _, err := sh.Store.Write.Exec(`DELETE FROM models WHERE name=?`, name); err != nil {
		return err
	}
	return sh.Cache.Reload()
}
