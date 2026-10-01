//go:build miniapp

package panel

import (
	"embed"
	"io/fs"
)

//go:generate sh -c "cd ../../web && npm run build"

// miniAppDist 是 web/ 的构建产物（npm --prefix web run build 输出到
// internal/panel/webdist）。产物不入库：只有带 -tags miniapp 编译
// （Docker/CI 会先构建前端）时才会嵌入；不带 tag 的构建走 miniapp_stub.go。
//
//go:embed all:webdist
var miniAppDist embed.FS

// miniAppDistFS 返回托管用的文件系统；index.html 不存在时返回 false，
// 由 Handler 渲染「前端未构建」占位页。
func miniAppDistFS() (fs.FS, bool) {
	sub, err := fs.Sub(miniAppDist, "webdist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
