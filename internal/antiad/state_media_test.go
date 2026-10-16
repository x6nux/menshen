package antiad

import (
	"testing"

	"menshen/internal/tg"
)

// TestHasMediaCarrier：has_media 是给模型的信号，必须反映这条消息是否真的
// 带媒体。判据若是“有配文但没正文”，会把纯图/纯贴纸报成无媒体，与实际相反。
func TestHasMediaCarrier(t *testing.T) {
	cases := []struct {
		name string
		m    *tg.Message
		want bool
	}{
		{"纯图", &tg.Message{Photo: []tg.PhotoSize{{FileID: "f"}}}, true},
		{"贴纸", &tg.Message{Sticker: &tg.Sticker{FileID: "s"}}, true},
		{"文件", &tg.Message{MsgPayload: tg.MsgPayload{Document: &tg.FileNamed{}}}, true},
		{"联系人卡片", &tg.Message{MsgPayload: tg.MsgPayload{Contact: &tg.Contact{}}}, true},
		{"纯文字", &tg.Message{Text: "网友正常聊天"}, false},
		{"只有配文的图", &tg.Message{Caption: "看这个", Photo: []tg.PhotoSize{{FileID: "f"}}}, true},
		// 只有媒体、没有文字的这几类同样要报成有媒体：它们不进判定正文
		// （进了的话每条语音都要花一次 AI），全靠这个信号让模型知道
		// 面前是一条语音/一条定位，而不是一条空消息。
		{"语音", &tg.Message{MsgPayload: tg.MsgPayload{Voice: &tg.MediaMeta{Duration: 3}}}, true},
		{"视频消息", &tg.Message{MsgPayload: tg.MsgPayload{VideoNote: &tg.MovingMedia{FileID: "v"}}}, true},
		{"实况照片", &tg.Message{MsgPayload: tg.MsgPayload{LivePhoto: &tg.LivePhoto{Photo: []tg.PhotoSize{{FileID: "f"}}}}}, true},
		{"付费媒体", &tg.Message{MsgPayload: tg.MsgPayload{PaidMedia: &tg.PaidMediaInfo{StarCount: 5}}}, true},
		{"定位", &tg.Message{MsgPayload: tg.MsgPayload{Location: &tg.Location{Latitude: 1}}}, true},
		{"骰子", &tg.Message{MsgPayload: tg.MsgPayload{Dice: &tg.Dice{Emoji: "🎲", Value: 3}}}, true},
	}
	for _, c := range cases {
		if got := hasMediaCarrier(c.m); got != c.want {
			t.Errorf("%s: hasMediaCarrier=%v，期望 %v", c.name, got, c.want)
		}
	}
}
