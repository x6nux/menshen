package antiad

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"menshen/internal/tg"
)

// ---- 抽帧：视频、动图、视频消息 ----
//
// 会动的媒体没有正文可判，只能看画面。广告的载体几乎都在开头几秒（一句卖点
// 加一个联系方式），后面是无关画面或黑屏，所以按固定时间点抽开头三帧送识图，
// 比整段解码或按均匀间隔抽帧都省，也正好落在投放的位置上。
//
// 抽帧依赖服务器上的 ffmpeg（纯 Go 没有 H.264 解码器）。它是可选增强：
// 没有 ffmpeg 时视频类按老行为放行，只在第一次遇到时提示一次。

const (
	// frameMaxSide 是抽帧后的长边上限，与识图取图的上限同源，避免整段视频
	// 按原分辨率送模型。
	frameMaxSide = visionMaxSide
	// ffmpegTimeout 是单次抽帧的总超时。抽帧跑在判定 worker 上，超时只占住
	// 一个 worker，但不能无限等：上游抖动时判定队列会跟着堵住。
	ffmpegTimeout = 25 * time.Second
)

// frameSeconds 是抽帧的时间点，单位秒：首帧加 1 秒、2 秒。
//
// 广告的卖点压在开头几秒，这三帧已经覆盖绝大多数投放形态，再往后抽只是
// 线性加 token。抽不到的时间点跳过（视频比它短），仍按抽到的帧判。
func frameSeconds() []int { return []int{0, 1, 2} }

// ffmpegReady 报告服务器上有没有 ffmpeg。
//
// 结果不缓存：一次 PATH 查找远比下载一段视频便宜，而缓存会让「装好 ffmpeg
// 之后要不要重启服务」变成一个隐藏前提。
func ffmpegReady() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// ffmpegMissingOnce 让「没有 ffmpeg」只提示一次：这是环境问题，不是某条
// 消息的问题，逐条提示只会把日志刷满。
var ffmpegMissingOnce sync.Once

// movingMediaOf 取出消息里会动的媒体，并给出它的形态标签。
//
// 三种形态（视频、动图、视频消息）走同一条抽帧路径，但标签要分开：判定
// 得看得出这是一条视频还是一个动图。没有文件句柄的不取 —— 那样连下载都
// 无从谈起，取了只会在识图里报一次无谓的失败。
func movingMediaOf(m *tg.Message) (*tg.MovingMedia, string) {
	switch {
	case m.Video != nil && m.Video.FileID != "":
		return m.Video, "［视频］"
	case m.Animation != nil && m.Animation.FileID != "":
		return m.Animation, "［动图］"
	case m.VideoNote != nil && m.VideoNote.FileID != "":
		return m.VideoNote, "［视频消息］"
	}
	return nil, ""
}

// frame 是抽出来的一帧：画面与它在视频里的时间点。
type frame struct {
	at   int
	jpeg []byte
}

// extractFrames 从视频里抽出指定时间点的帧，返回按时间升序的结果。
//
// 单个时间点抽不到（视频比它短、该处没有可解的帧）就跳过；只要还剩一帧就
// 不算失败 —— 抽帧失败与识图失败同向，一帧看个大概也比整条不判强。全抽不到
// 才返回错误。
func extractFrames(video []byte, at []int) ([]frame, error) {
	dir, err := os.MkdirTemp("", "menshen-frames")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "in")
	if err := os.WriteFile(src, video, 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()

	var out []frame
	var lastErr error
	for _, sec := range at {
		dst := filepath.Join(dir, fmt.Sprintf("f%d.jpg", sec))
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-nostdin", "-hide_banner", "-loglevel", "error",
			// -ss 放在 -i 前面：按关键帧快速定位，不必解码整段。
			"-ss", strconv.Itoa(sec),
			"-i", src,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", frameMaxSide),
			"-y", dst)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			lastErr = fmt.Errorf("抽 %d 秒帧失败：%v（%s）", sec, err, firstLine(stderr.String()))
			continue
		}
		jpeg, err := os.ReadFile(dst)
		if err != nil || len(jpeg) == 0 {
			lastErr = fmt.Errorf("抽 %d 秒帧没有产物", sec)
			continue
		}
		out = append(out, frame{at: sec, jpeg: jpeg})
	}
	if len(out) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("没有可抽的时间点")
		}
		return nil, lastErr
	}
	return out, nil
}

// firstLine 取进程输出的第一行，用于错误信息：ffmpeg 的报错常常几十行，
// 全带进来只会把日志撑满。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
