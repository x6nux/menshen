package antiad

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// newFileServer 起一个只回固定字节的假 TG 文件服务器，返回它的地址。
func newFileServer(t *testing.T, status int, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// testVideo 用 ffmpeg 现造一段测试视频，返回字节。没有 ffmpeg 的机器跳过 ——
// 抽帧依赖它，但它是环境依赖，不该让整套测试在没装的机器上失败。
func testVideo(t *testing.T, seconds int) []byte {
	t.Helper()
	if !ffmpegReady() {
		t.Skip("没有 ffmpeg，跳过抽帧相关用例")
	}
	out := filepath.Join(t.TempDir(), "in.mp4")
	// testsrc 画面里带帧计数与文字，模型能读出东西；-pix_fmt yuv420p 保证
	// 宽高为偶数，mjpeg 编码器才肯收。
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration="+itoa(int64(seconds))+":size=320x180:rate=10",
		"-pix_fmt", "yuv420p", "-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("本机 ffmpeg 造不出测试视频（%v：%s）", err, firstLine(string(b)))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// videoFiles 让假 TG 把给定字节当作视频下发。
func videoFiles(t *testing.T, b *core.Bot, fake *testutil.FakeTG, video []byte) {
	t.Helper()
	b.Cfg.TGAPIBase = newFileServer(t, http.StatusOK, video)
	fake.Resp["getFile"] = `{"ok":true,"result":{"file_path":"videos/x.mp4"}}`
}

// videoMsg 造一条不带配文的视频消息。
func videoMsg(msgID int64, unique string, size int64) *tg.Message {
	m := testutil.GroupMsg(-100, 42, msgID, "")
	m.Video = &tg.MovingMedia{
		FileID: "vid", FileUniqueID: unique, FileSize: size, Duration: 3, Width: 320, Height: 180,
	}
	return m
}

// TestVisualOfMovingMedia：视频、动图、视频消息都要抽帧识图，标签分得清形态；
// 没有文件句柄的不取（连下载都无从谈起）。
func TestVisualOfMovingMedia(t *testing.T) {
	cases := []struct {
		name  string
		m     *tg.Message
		label string
	}{
		{"视频", func() *tg.Message {
			m := testutil.GroupMsg(-100, 42, 1, "")
			m.Video = &tg.MovingMedia{FileID: "v", FileUniqueID: "vu"}
			return m
		}(), "［视频］"},
		{"动图", func() *tg.Message {
			m := testutil.GroupMsg(-100, 42, 1, "")
			m.Animation = &tg.MovingMedia{FileID: "a", FileUniqueID: "au"}
			return m
		}(), "［动图］"},
		{"视频消息", func() *tg.Message {
			m := testutil.GroupMsg(-100, 42, 1, "")
			m.VideoNote = &tg.MovingMedia{FileID: "n", FileUniqueID: "nu"}
			return m
		}(), "［视频消息］"},
	}
	for _, c := range cases {
		v, ok := visualOf(c.m)
		if !ok {
			t.Fatalf("%s：应当抽帧识图", c.name)
		}
		if v.label != c.label {
			t.Errorf("%s：标签 = %q，期望 %q", c.name, v.label, c.label)
		}
		want := []int{0, 1, 2}
		if len(v.frames) != len(want) {
			t.Fatalf("%s：抽帧时间点 = %v，期望 %v", c.name, v.frames, want)
		}
		for i := range want {
			if v.frames[i] != want[i] {
				t.Errorf("%s：抽帧时间点 = %v，期望 %v", c.name, v.frames, want)
			}
		}
	}
	// 只有元数据、没有文件句柄：取不到内容，不该认成可识别的载荷。
	m := testutil.GroupMsg(-100, 42, 1, "")
	m.Video = &tg.MovingMedia{FileSize: 1024}
	if _, ok := visualOf(m); ok {
		t.Error("没有 file_id 的视频不该抽帧")
	}
}

// TestExtractFramesShortVideo：比时间点短的视频，抽不到的那一帧跳过，剩下的
// 照样用 —— 一秒的动图也得判，不该因为 2 秒处没画面就整条不判。
func TestExtractFramesShortVideo(t *testing.T) {
	video := testVideo(t, 1)
	frames, err := extractFrames(video, []int{0, 1, 2})
	if err != nil {
		t.Fatalf("短视频应抽出可用帧：%v", err)
	}
	if len(frames) == 0 || len(frames) >= 3 {
		t.Errorf("1 秒视频应抽出 1~2 帧，实际 %d 帧", len(frames))
	}
	if frames[0].at != 0 {
		t.Errorf("第一帧应是首帧，实际 %d 秒", frames[0].at)
	}
	for _, f := range frames {
		if len(f.jpeg) == 0 {
			t.Error("抽出的帧是空的")
		}
	}
}

// TestExtractFramesRejectsNonVideo：不是视频的字节抽不出帧，返回错误而不是
// 空结果 —— 调用方据此走识图失败的同一条路。
func TestExtractFramesRejectsNonVideo(t *testing.T) {
	if !ffmpegReady() {
		t.Skip("没有 ffmpeg，跳过抽帧相关用例")
	}
	if _, err := extractFrames([]byte("not a video"), []int{0}); err == nil {
		t.Error("非视频字节应抽帧失败")
	}
}

// TestVideoFramesJudgedWithVision：不带配文的视频以抽帧识图的结果进判定正文，
// 三帧一次请求带上去，留底也随之更新。同一段视频按 file_unique_id 只识一次。
func TestVideoFramesJudgedWithVision(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	videoFiles(t, b, fake, testVideo(t, 3))

	seen, visionN := new(atomic.Value), new(atomic.Int32)
	seen.Store("")
	visionBody := new(atomic.Value)
	visionBody.Store("")
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/systemone"):
			seen.Store(string(body))
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
		case strings.Contains(string(body), `"vision-model"`):
			visionN.Add(1)
			visionBody.Store(string(body))
			w.Write([]byte(`{"choices":[{"message":{"content":"开头三帧写着：加微信 abc123"}}]}`))
		default:
			w.Write([]byte(llmReply(false, 0.9, "none", "message")))
		}
	})
	if err := b.PutSetting("antiad_vision_model", "vision-model"); err != nil {
		t.Fatal(err)
	}

	HandleGroupMessage(b, videoMsg(1, "vu1", 1024))
	waitIdle(t, b)

	if s := seen.Load().(string); !strings.Contains(s, "［视频］") || !strings.Contains(s, "abc123") {
		t.Fatalf("送检正文应带抽帧识图结果: %s", s)
	}
	var text string
	b.Store.Read.QueryRow(`SELECT text FROM group_messages WHERE message_id=1`).Scan(&text)
	if !strings.Contains(text, "abc123") {
		t.Errorf("留底应带抽帧识图结果，得到 %q", text)
	}

	// 请求里三帧各带一条 image_url，且带上时间点标注：模型要分得清哪段文字
	// 出自哪一帧。system 的 content 是字符串、user 的是数组，所以直接数
	// JSON 里出现的次数，不按消息结构解。
	body := visionBody.Load().(string)
	if n := strings.Count(body, "data:image/jpeg;base64,"); n != 3 {
		t.Errorf("三帧应各带一张图，实际 %d 张", n)
	}
	for _, w := range []string{"at 0 s", "at 1 s", "at 2 s"} {
		if !strings.Contains(body, w) {
			t.Errorf("请求里缺帧的时间点标注 %q", w)
		}
	}
	if !strings.Contains(body, "video content reporter") {
		t.Error("抽帧识图应走视频专用提示词")
	}
	if !strings.Contains(body, `"data:image/jpeg;base64,`) {
		t.Error("帧应以 JPEG data URL 送出")
	}

	HandleGroupMessage(b, videoMsg(2, "vu1", 1024))
	waitIdle(t, b)
	if visionN.Load() != 1 {
		t.Errorf("同一段视频应只识一次，实际 %d 次", visionN.Load())
	}
}

// TestVideoOversizeSkipsVision：超过 getFile 上限的视频不下载、不抽帧，
// 记一条识图失败 —— 提前拦下比让上游必然失败一次要好。
func TestVideoOversizeSkipsVision(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	videoFiles(t, b, fake, testVideo(t, 3))
	_, visionN := visionAI(t, b)

	HandleGroupMessage(b, videoMsg(1, "vuBig", visionFileMax+1))
	waitIdle(t, b)

	if visionN.Load() != 0 {
		t.Error("超限视频不该送识图")
	}
	if n := fake.CountCalls("getFile"); n != 0 {
		t.Errorf("超限视频不该下载，实际 getFile %d 次", n)
	}
	if _, _, reason := logRow(t, b); !strings.Contains(reason, "识图失败") {
		t.Errorf("该记识图失败，实际理由 %q", reason)
	}
}

// TestNoFFmpegSkipsVideoVision：服务器上没有 ffmpeg 时视频类不判，按老行为
// 放行，且不逐条记失败流水 —— 这是环境问题，不是这条消息的问题。
func TestNoFFmpegSkipsVideoVision(t *testing.T) {
	// 先用真的 ffmpeg 造出视频，再把 PATH 掏空，模拟服务器上没装。
	video := testVideo(t, 3)
	t.Setenv("PATH", t.TempDir())
	if ffmpegReady() {
		t.Skip("本机 PATH 隔离没生效，跳过")
	}
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	videoFiles(t, b, fake, video)
	seen, visionN := visionAI(t, b)

	HandleGroupMessage(b, videoMsg(1, "vu2", 1024))
	waitIdle(t, b)

	if visionN.Load() != 0 {
		t.Error("没有 ffmpeg 时不该发起识图")
	}
	if seen.Load().(string) != "" {
		t.Error("没有 ffmpeg 时纯视频不该送检")
	}
	if _, _, reason := logRow(t, b); strings.Contains(reason, "识图失败") {
		t.Errorf("缺 ffmpeg 不该记成识图失败，实际理由 %q", reason)
	}
}
