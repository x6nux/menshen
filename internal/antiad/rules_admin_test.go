package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestEnforceNeedsTestRun 固定从未跑过全库测试不许开强制这一不变量：
// last_fp 的默认 0 只代表没测出误封，不代表测过且干净。AI 直接
// 写库的候选即为此形态 —— 若能直接开强制，就绕过了防误封不变量。
func TestEnforceNeedsTestRun(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	res, err := b.Store.Write.Exec(`INSERT INTO ad_rules
		(name,pattern,category,note,source,enabled,enforce,last_fp,last_undone,
		 last_tested_at,created_at,created_by)
		VALUES ('r','未测词','','','ai',1,0,0,0,0,0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	err = SetRuleEnforce(b.Shared, id, true)
	if err == nil || !strings.Contains(err.Error(), "还没跑过全库测试") {
		t.Fatalf("没测过的规则开强制应被拒并说明原因，得到 %v", err)
	}

	if _, err := TestRuleByID(b.Shared, id); err != nil {
		t.Fatal(err)
	}
	if err := SetRuleEnforce(b.Shared, id, true); err != nil {
		t.Fatalf("测过且干净后应允许开强制，得到 %v", err)
	}
}
