// Mini App 与管理面板的 go:embed 嵌入口（默认编译，不需要 build tag）：
// web/ 的构建产物输出到 internal/panel/webdist。Mini App 入口（base /miniapp/）
// 先构建，管理面板入口（base /admin/）后构建，两者共用同一产物目录：
// index.html/public.html 属于 Mini App，admin.html 属于管理面板。
// 构建会写回占位文件 .gitkeep（入库），保证全新克隆在未构建前端时也能
// go build / go test；此时两个入口页都不存在，运行时返回前端未构建提示页。
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

// distFS 返回产物文件系统；未构建（连目录都取不到）时返回 false。
func distFS() (fs.FS, bool) {
	sub, err := fs.Sub(miniAppDist, "webdist")
	if err != nil {
		return nil, false
	}
	return sub, true
}

// miniAppDistFS 返回托管 Mini App 用的文件系统；index.html 不存在时返回
// false，由 Handler 渲染 `前端未构建` 占位页。
func miniAppDistFS() (fs.FS, bool) {
	sub, ok := distFS()
	if !ok {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// adminDistFS 返回托管管理面板用的文件系统；admin.html 不存在时返回 false，
// 由 Handler 渲染 `管理面板未构建` 占位页。
func adminDistFS() (fs.FS, bool) {
	sub, ok := distFS()
	if !ok {
		return nil, false
	}
	if _, err := fs.Stat(sub, "admin.html"); err != nil {
		return nil, false
	}
	return sub, true
}
