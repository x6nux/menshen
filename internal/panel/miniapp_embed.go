// Mini App 的 go:embed 嵌入口（默认编译，不需要 build tag）：
// web/ 的构建产物输出到 internal/panel/webdist，npm run build 会顺带写回
// 一个占位文件 .gitkeep（该文件入库，保证全新克隆在没跑前端构建时也能
// go build / go test——此时 index.html 不存在，运行时给「前端未构建」提示页）。
package panel

import (
	"embed"
	"io/fs"
)

//go:generate sh -c "cd ../../web && npm run build"

// miniAppDist 是 web/ 的构建产物；未构建时只有 .gitkeep。
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
