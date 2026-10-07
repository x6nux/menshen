package antiad

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// pngBytes 是一个最小的 PNG 头，够 http.DetectContentType 认成图片。
var pngBytes = []byte("\x89PNG\r\n\x1a\n0000000000000000")

// fakeFiles 起一个假的 TG 文件服务器，返回下载次数计数器。
func fakeFiles(t *testing.T, b *core.Bot, fake *testutil.FakeTG, status int) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
		w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)
	b.Cfg.TGAPIBase = srv.URL
	fake.Resp["getFile"] = `{"ok":true,"result":{"file_path":"photos/x.png"}}`
	return &n
}

// visionAI 起假上游：识图模型回一段描述，systemone 记下收到的正文。
func visionAI(t *testing.T, b *core.Bot) (seen *atomic.Value, visionN *atomic.Int32) {
	t.Helper()
	seen, visionN = new(atomic.Value), new(atomic.Int32)
	seen.Store("")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/systemone"):
			seen.Store(string(body))
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
		case strings.Contains(string(body), `"vision-model"`):
			visionN.Add(1)
			w.Write([]byte(`{"choices":[{"message":{"content":"图中文字：加微信 abc123\n领福利"}}]}`))
		default:
			w.Write([]byte(llmReply(false, 0.9, "none", "message")))
		}
	})
	if err := b.PutSetting("antiad_vision_model", "vision-model"); err != nil {
		t.Fatal(err)
	}
	return seen, visionN
}

func photoMsg(msgID int64, unique string) *tg.Message {
	m := testutil.GroupMsg(-100, 42, msgID, "")
	m.Photo = []tg.PhotoSize{
		{FileID: "small", FileUniqueID: unique + "s", Width: 90, Height: 90},
		{FileID: "mid", FileUniqueID: unique, Width: 1280, Height: 720},
		{FileID: "big", FileUniqueID: unique + "b", Width: 2560, Height: 1440},
	}
	return m
}

// TestVisualOf：图片取不超过上限的最大一档；静态贴纸看原图，动态贴纸看缩略图。
func TestVisualOf(t *testing.T) {
	if v, ok := visualOf(photoMsg(1, "u")); !ok || v.fileID != "mid" {
		t.Errorf("图片应取 mid 档，得到 %+v", v)
	}
	m := testutil.GroupMsg(-100, 42, 1, "")
	m.Sticker = &tg.Sticker{FileID: "s", FileUniqueID: "su", IsAnimated: true, Emoji: "😀",
		SetName: "赚钱群", Thumbnail: &tg.PhotoSize{FileID: "th", FileUniqueID: "thu"}}
	v, ok := visualOf(m)
	if !ok || v.fileID != "th" || !strings.Contains(v.label, "赚钱群") {
		t.Errorf("动态贴纸应看缩略图并带包名，得到 %+v", v)
	}
	if _, ok := visualOf(testutil.GroupMsg(-100, 42, 1, "纯文字")); ok {
		t.Error("没有图不该识图")
	}
}

// TestPurePhotoNeedsVisionModel：没配识图模型时纯图仍不判定，零额外开销。
func TestPurePhotoNeedsVisionModel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, _ := fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	HandleGroupMessage(b, photoMsg(1, "u1"))
	waitIdle(t, b)
	if soN.Load() != 0 {
		t.Error("没配识图模型时纯图不该送检")
	}
}

// TestPurePhotoJudgedWithVision：配了识图模型，图里的文字以`［图片］`行并进正文送检，
// 留底也带上；同一张图按 file_unique_id 只识一次。
func TestPurePhotoJudgedWithVision(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	seen, visionN := visionAI(t, b)
	downloads := fakeFiles(t, b, fake, 200)

	HandleGroupMessage(b, photoMsg(1, "u1"))
	waitIdle(t, b)
	if s := seen.Load().(string); !strings.Contains(s, "［图片］") || !strings.Contains(s, "abc123") {
		t.Fatalf("送检正文应带识图结果: %s", s)
	}
	var text string
	b.Store.Read.QueryRow(`SELECT text FROM group_messages WHERE message_id=1`).Scan(&text)
	if !strings.Contains(text, "abc123") {
		t.Errorf("留底应带识图结果，得到 %q", text)
	}
	if p := fake.LastCall("getFile"); p["file_id"] != "mid" {
		t.Errorf("应下载 mid 档，得到 %v", p)
	}

	HandleGroupMessage(b, photoMsg(2, "u1"))
	waitIdle(t, b)
	if visionN.Load() != 1 || downloads.Load() != 1 {
		t.Errorf("同一张图应只识一次：识图 %d 次、下载 %d 次", visionN.Load(), downloads.Load())
	}
}

// TestVisionFallsBackToReasoning：推理模型偶发把话全说在思考里、正文为空。
// 思考同样是模型对图片的描述，拿来当描述用，比整条判定失败放行更好。
func TestVisionFallsBackToReasoning(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	seen := new(atomic.Value)
	seen.Store("")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/systemone"):
			seen.Store(string(body))
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
		case strings.Contains(string(body), `"vision-model"`):
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"图里有一行文字 abc123\"}}]}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		default:
			w.Write([]byte(llmReply(false, 0.9, "none", "message")))
		}
	})
	if err := b.PutSetting("antiad_vision_model", "vision-model"); err != nil {
		t.Fatal(err)
	}
	fakeFiles(t, b, fake, 200)

	HandleGroupMessage(b, photoMsg(1, "uR"))
	waitIdle(t, b)
	if s := seen.Load().(string); !strings.Contains(s, "abc123") {
		t.Errorf("只有思考内容时也应把描述并进正文，得到 %s", s)
	}
}

// TestVisionEmptyResponseRetries：一次空响应（连思考都没有）应重试一次；
// 第二次正常就救回来。两次都空才判失败。
func TestVisionEmptyResponseRetries(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	var visionN atomic.Int32
	seen := new(atomic.Value)
	seen.Store("")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/systemone"):
			seen.Store(string(body))
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
		case strings.Contains(string(body), `"vision-model"`):
			w.Header().Set("Content-Type", "text/event-stream")
			if visionN.Add(1) == 1 {
				w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
			} else {
				w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"重试后的描述 abc123\"}}]}\n\n"))
			}
			w.Write([]byte("data: [DONE]\n\n"))
		default:
			w.Write([]byte(llmReply(false, 0.9, "none", "message")))
		}
	})
	if err := b.PutSetting("antiad_vision_model", "vision-model"); err != nil {
		t.Fatal(err)
	}
	fakeFiles(t, b, fake, 200)

	HandleGroupMessage(b, photoMsg(1, "uE"))
	waitIdle(t, b)
	if n := visionN.Load(); n != 2 {
		t.Errorf("空响应应重试一次，识图请求 %d 次", n)
	}
	if s := seen.Load().(string); !strings.Contains(s, "重试后的描述") {
		t.Errorf("重试成功的描述应并进正文，得到 %s", s)
	}
}

// TestVisionFailureLetsPass：识图失败与判定失败同向，放行；下载错误里不得带 token。
func TestVisionFailureLetsPass(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	seen, _ := visionAI(t, b)
	fakeFiles(t, b, fake, 500)

	HandleGroupMessage(b, photoMsg(1, "u1"))
	waitIdle(t, b)
	if seen.Load().(string) != "" {
		t.Error("识图失败的纯图不该送检")
	}
	verdict, _, reason := logRow(t, b)
	if verdict != "error" || !strings.Contains(reason, "识图失败") {
		t.Errorf("verdict=%q reason=%q", verdict, reason)
	}

	b.Cfg.TGAPIBase = "http://127.0.0.1:1"
	_, err := b.DownloadFile("photos/x.png")
	if err == nil || strings.Contains(err.Error(), testutil.TestToken) {
		t.Errorf("下载错误不得带 token: %v", err)
	}
}
