//go:build !miniapp

package panel

import "io/fs"

// miniAppDistFS 在未带 -tags miniapp 编译时恒为缺失：仓库不提交前端产物，
// 本地 go build / go test 因此不依赖 Node 与 webdist。
// 需要真实前端时先 npm --prefix web run build，再带 -tags miniapp 编译。
func miniAppDistFS() (fs.FS, bool) { return nil, false }
