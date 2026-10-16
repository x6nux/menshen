package antiad

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"menshen/internal/logbuf"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// msgTextOf 解析一条消息 JSON，返回它进判定的正文。
func msgTextOf(t *testing.T, js string) string {
	t.Helper()
	var m tg.Message
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("解析失败 %s: %v", js, err)
	}
	return msgText(&m)
}

// warnRecords 接住 slog 的警告并保留字段，用来核对留档的原始报文。
// slog 默认 logger 是进程级的，测完恢复，避免污染同包其他测试。
func warnRecords(t *testing.T) func() []logbuf.Record {
	t.Helper()
	prev := slog.Default()
	buf := logbuf.New(50)
	slog.SetDefault(slog.New(logbuf.NewHandler(buf, nil, slog.LevelWarn)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.Snapshot
}

// TestMsgTextRichMessage：富消息的正文全在块里，text 与 reply_markup 都是
// 空的，逐块渲染是它唯一的取值口径。不渲染就等于把一条有正文、有链接、
// 有按钮的广告当成空消息放行。
func TestMsgTextRichMessage(t *testing.T) {
	js := `{"rich_message":{"blocks":[
		{"type":"heading","text":"NEW PREMIUM DROP"},
		{"type":"paragraph","text":[{"type":"bold","text":"看这里"},
			{"type":"url","text":"点此","url":"https://ad.example/go"}]},
		{"type":"list","items":[{"label":"UNCENSORED",
			"blocks":[{"type":"paragraph","text":"高清水印"}]}]},
		{"type":"collage","blocks":[{"type":"photo","photo":[{"file_id":"x"}],
			"caption":{"text":"相册配文"}}]},
		{"type":"buttons","buttons":[{"text":"VIEW CONTENT","url":"https://ad.example/btn"}]},
		{"type":"mystery_block","payload":"z"}
	]}}`
	got := msgTextOf(t, js)
	for _, w := range []string{
		"NEW PREMIUM DROP",
		"看这里", "https://ad.example/go",
		"UNCENSORED", "高清水印",
		"［富消息·媒体］图片 1 张", "［配文］相册配文",
		"［按钮］VIEW CONTENT https://ad.example/btn",
		// 新块类型读不到内容，也要看得见有东西 —— 类型名是补渲染的入口。
		"［富消息·未识别的块］mystery_block",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("富消息正文缺 %q：\n%s", w, got)
		}
	}
}

// TestMsgTextNewPayloads：这些载荷字段的文字全由发送方填写，解码时丢掉
// 任何一类都等于开了一条不写正文也能投放的通道，必须都进正文。
func TestMsgTextNewPayloads(t *testing.T) {
	cases := []struct {
		name string
		js   string
		want []string
	}{
		{"付费媒体",
			`{"paid_media":{"star_count":500,"paid_media":[{"type":"photo","caption":"解锁看全部"}]}}`,
			[]string{"［付费媒体］1 项，500 星", "解锁看全部"}},
		{"抽奖", `{"giveaway":{"winner_count":5,"prize_description":"送会员"}}`,
			[]string{"［抽奖］送会员 5 名"}},
		{"开奖", `{"giveaway_winners":{"winner_count":3,"prize_description":"送会员"}}`,
			[]string{"［开奖］送会员 3 名"}},
		{"礼物", `{"gift":{"gift":{"id":"A1","star_count":15},"text":"加我领"}}`,
			[]string{"［礼物］A1 15 星 加我领"}},
		{"礼物升级", `{"gift_upgrade_sent":{"gift":{"id":"B2","star_count":25}}}`,
			[]string{"［礼物升级］B2 25 星"}},
		{"独特礼物", `{"unique_gift":{"gift":{"name":"星空","base_name":"星空","gift_id":"g1"}}}`,
			[]string{"［独特礼物］星空 g1"}},
		{"抽奖开始", `{"giveaway_created":{}}`, []string{"［抽奖开始］"}},
		{"抽奖结束", `{"giveaway_completed":{"winner_count":2}}`, []string{"［抽奖结束］2 名中奖"}},
		{"分享用户",
			`{"users_shared":{"users":[{"user_id":9,"first_name":"客服","username":"kf"}]}}`,
			[]string{"［分享用户］1 人 客服 @kf"}},
		{"分享聊天", `{"chat_shared":{"chat_id":-1,"title":"引流号","username":"yl"}}`,
			[]string{"［分享聊天］引流号 @yl"}},
		{"网页数据", `{"web_app_data":{"data":"d","button_text":"领券"}}`,
			[]string{"［网页数据］领券 d"}},
		{"支付",
			`{"successful_payment":{"currency":"XTR","total_amount":100,"invoice_payload":"pay",
				"order_info":{"name":"张三","phone_number":"+86","shipping_address":{"city":"深圳"}}}}`,
			[]string{"［支付］pay 100 XTR 张三 +86 深圳"}},
		{"退款", `{"refunded_payment":{"currency":"XTR","total_amount":100,"invoice_payload":"pay"}}`,
			[]string{"［退款］pay 100 XTR"}},
		{"雷达提醒",
			`{"proximity_alert_triggered":{"traveler":{"id":1,"username":"a"},
				"watcher":{"id":2,"username":"b"},"distance":30}}`,
			[]string{"［雷达提醒］@a @b 30 米"}},
		{"登录网站", `{"connected_website":"ad.example"}`, []string{"［登录网站］ad.example"}},
		{"付费消息", `{"paid_message_price_changed":{"paid_message_star_count":9}}`,
			[]string{"［付费消息］9 星"}},
		{"投票新增选项", `{"poll_option_added":{"option":{"text":"加微信"}}}`,
			[]string{"［投票新增选项］加微信"}},
		{"投票删除选项", `{"poll_option_deleted":{"option":{"text":"旧选项"}}}`,
			[]string{"［投票删除选项］旧选项"}},
		{"清单新增任务", `{"checklist_tasks_added":{"tasks":[{"text":"转发三个群"}]}}`,
			[]string{"［清单新增任务］转发三个群"}},
		{"清单完成任务", `{"checklist_tasks_done":{"tasks":[{"text":"加群"}]}}`,
			[]string{"［清单完成任务］加群"}},
		{"群改名", `{"new_chat_title":"本群禁止广告 加V看片"}`,
			[]string{"［群改名］本群禁止广告 加V看片"}},
		{"置顶消息", `{"pinned_message":{"text":"置顶广告正文"}}`,
			[]string{"［置顶消息］置顶广告正文"}},
	}
	for _, c := range cases {
		got := msgTextOf(t, c.js)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：正文缺 %q，实际 %q", c.name, w, got)
			}
		}
	}
}

// TestHasPayloadFlagsUnknownType：「载荷全不可识别」是客户端能渲染、我们解
// 不出来的新消息类型的形态。判据要能把它认出来（据此留原始报文），又不能
// 把正常消息误报成它。
func TestHasPayloadFlagsUnknownType(t *testing.T) {
	cases := []struct {
		name string
		js   string
		want bool
	}{
		{"只有转发来源", `{"forward_origin":{"type":"hidden_user","sender_user_name":"引流号"}}`, false},
		{"完全陌生的类型", `{"some_new_media":{"x":1}}`, false},
		{"富消息", `{"rich_message":{"blocks":[{"type":"paragraph","text":"a"}]}}`, true},
		{"无配文图片", `{"photo":[{"file_id":"x"}]}`, true},
		{"无配文视频", `{"video":{"file_id":"x"}}`, true},
		{"语音", `{"voice":{"file_id":"x","duration":3}}`, true},
		{"实况照片", `{"live_photo":{"photo":[{"file_id":"x"}]}}`, true},
		{"付费媒体", `{"paid_media":{"star_count":1,"paid_media":[]}}`, true},
		{"定位", `{"location":{"latitude":1,"longitude":2}}`, true},
		{"骰子", `{"dice":{"emoji":"🎲","value":3}}`, true},
		{"换群头像", `{"new_chat_photo":[{"file_id":"x"}]}`, true},
		{"进群消息", `{"new_chat_members":[{"id":1}]}`, true},
		{"内联按钮", `{"reply_markup":{"inline_keyboard":[[{"text":"t","url":"u"}]]}}`, true},
		// 回复消息本身必然带内容，只看到引用说明那份内容没解出来。
		{"只有外部引用", `{"external_reply":{"text":"x"}}`, false},
		{"空白正文", `{"text":"你好"}`, true},
	}
	for _, c := range cases {
		var m tg.Message
		if err := json.Unmarshal([]byte(c.js), &m); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := hasPayload(&m); got != c.want {
			t.Errorf("%s: hasPayload=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestUnknownPayloadLoggedOnce：载荷不可识别的消息要留下原始报文，且同一群
// 每分钟只留一条 —— 新类型出现时往往是同一波刷屏，逐条落盘只会淹没日志。
//
// 这条日志是这类缺口唯一的暴露方式：判定侧拿不到正文，只能放行，事后回看
// 流水看不出问题。
func TestUnknownPayloadLoggedOnce(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	recs := warnRecords(t)

	send := func(msgID int64, raw string) {
		m := testutil.GroupMsg(-100, 777, msgID, "")
		m.ForwardOrigin = json.RawMessage(`{"type":"hidden_user","sender_user_name":"引流号"}`)
		m.Raw = json.RawMessage(raw)
		dispatch(b, &tg.Update{Message: m})
	}
	send(10, `{"message_id":10,"forward_origin":{"type":"hidden_user"},
		"mystery_media":{"x":1}}`)
	send(11, `{"message_id":11,"forward_origin":{"type":"hidden_user"},
		"mystery_media":{"x":2}}`)

	var n int
	var raw string
	for _, r := range recs() {
		if !strings.Contains(r.Message, "载荷不可识别") {
			continue
		}
		n++
		for _, a := range r.Attrs {
			if a.Key == "raw" {
				raw = a.Value
			}
		}
	}
	if n != 1 {
		t.Errorf("同一群每分钟只该留一条，实际 %d 条", n)
	}
	if !strings.Contains(raw, "mystery_media") {
		t.Errorf("留档里没有原始报文的字段名：%q", raw)
	}
	waitIdle(t, b)
}
