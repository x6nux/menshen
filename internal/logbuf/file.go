package logbuf

// 运行日志落盘：与 SQLite 库文件同目录、同前缀的滚动日志文件。
//
// 标准输出在容器部署里往往没人盯（docker logs 有驱动上限，出问题第一
// 反应也是「进容器看文件」），排查需要一份本地可查、大小可控的文件。
// 滚动策略两条：
//   - 单文件写到 maxFile 字节就把整个文件挪成 .1 备份（.1→.2→… 依次上移）；
//   - 备份总量按「总上限 − 单文件上限」执行 —— 当前文件永远 ≤ 单文件
//     上限，于是「当前 + 备份」不超总上限；总上限 = 单文件上限时不留备份。
//
// 上限由调用方每次写入时提供（main 里读全局设置）：面板上改小立刻生效并
// 触发清理，任一为 0 关闭文件日志。Write 永不返回错误、也绝不 panic：
// 日志写不进去不能反过来打断业务，打不开文件按退避重试并在 stderr 提示。

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogPathFor 给出与数据库文件同目录、同前缀的日志路径：
// data.db → data.log（去扩展名换 .log），无扩展名直接追加。
func LogPathFor(dbPath string) string {
	dir, name := filepath.Split(dbPath)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" {
		base = name
	}
	return filepath.Join(dir, base+".log")
}

// reopenBackoff 是打开失败后的重试间隔：磁盘满 / 权限错时每条日志都重试，
// 只是把 CPU 烧在必然失败的 syscall 上。
const reopenBackoff = 30 * time.Second

// RotatingFile 是按大小滚动的日志文件，实现 io.Writer。
type RotatingFile struct {
	mu     sync.Mutex
	path   string
	limits func() (maxFile, maxTotal int64)

	file *os.File
	size int64

	lastFile  int64 // 上次见到的单文件上限，检测运行中的改动
	lastTotal int64
	seen      bool      // 是否读过一次 limits（首次不算「改动」）
	failAt    time.Time // 上次打开失败的时刻；退避期内不再尝试
}

// NewRotatingFile 建一个滚动日志文件。limits 每次写入时调用，返回单文件
// 与总大小上限（字节）；任一 ≤ 0 表示关闭文件日志。
func NewRotatingFile(path string, limits func() (maxFile, maxTotal int64)) *RotatingFile {
	return &RotatingFile{path: path, limits: limits}
}

// Write 写入一条（slog 的每条记录是一次 Write）。错误一律不上抛：
// io.MultiWriter 里有一个 writer 报错就会把错误带回 slog，而日志链路
// 不该被磁盘问题打断；丢的这行标准输出上还有。
func (f *RotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	maxFile, maxTotal := f.limits()
	if maxFile <= 0 || maxTotal <= 0 {
		// 0 = 关闭：关掉已打开的句柄（下次打开继续追加），丢弃本次写入。
		f.closeLocked()
		f.lastFile, f.lastTotal, f.seen = maxFile, maxTotal, true
		return len(p), nil
	}
	if maxTotal < maxFile {
		// 滚动以单文件上限为粒度，总上限小于它物理上做不到，取等号
		//（等价于不留备份）。
		maxTotal = maxFile
	}
	prevTotal := f.lastTotal
	f.lastFile, f.lastTotal, f.seen = maxFile, maxTotal, true
	if f.seen && maxTotal < prevTotal {
		// 总上限被调小：不等下一次滚动，立刻按新上限清备份。
		f.cleanupLocked(maxFile, maxTotal)
	}

	if f.file == nil && !f.openLocked() {
		return len(p), nil
	}
	if f.size > 0 && f.size+int64(len(p)) > maxFile {
		f.rotateLocked()
		if f.file == nil {
			return len(p), nil // 重开失败（已进退避），这行只上标准输出
		}
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	if err != nil {
		// 下次写入时重开；openLocked 的退避挡住磁盘持续故障时的空转。
		f.closeLocked()
	}
	return len(p), nil
}

// Close 关闭当前文件（进程退出时调用；幂等）。
func (f *RotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeLocked()
	return nil
}

// openLocked 打开（或重新打开）当前文件，追加模式。成功后按当前上限清理
// 一次备份 —— 重启时也要把上个进程留下的超量文件压回去。
func (f *RotatingFile) openLocked() bool {
	if !f.failAt.IsZero() && time.Since(f.failAt) < reopenBackoff {
		return false
	}
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		if f.failAt.IsZero() || time.Since(f.failAt) >= reopenBackoff {
			fmt.Fprintf(os.Stderr, "menshen: 打开日志文件失败（%s）：%v\n", f.path, err)
		}
		f.failAt = time.Now()
		return false
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		f.failAt = time.Now()
		return false
	}
	f.file = file
	f.size = info.Size()
	f.failAt = time.Time{}
	f.cleanupLocked(f.lastFile, f.lastTotal)
	return true
}

// rotateLocked 把当前文件挪成 .1 备份并打开新文件。rename 失败不阻断：
// 最坏情况是这一轮没滚成，下一行写入时 size 检查会再试。
func (f *RotatingFile) rotateLocked() {
	f.closeLocked()
	// 现有备份 .1..k 依次上移成 .2..k+1，腾出 .1 给当前文件。
	n := 0
	for i := 1; ; i++ {
		if _, err := os.Stat(backupPath(f.path, i)); err != nil {
			n = i - 1
			break
		}
	}
	for i := n; i >= 1; i-- {
		os.Rename(backupPath(f.path, i), backupPath(f.path, i+1))
	}
	os.Rename(f.path, backupPath(f.path, 1))
	f.openLocked()
}

// cleanupLocked 把备份总量压回「总上限 − 单文件上限」以内（给当前文件留
// 一个单文件的余量），从最旧的备份开始删；当前文件永远保留。删除失败
// 就到此为止，下轮滚动再试。
func (f *RotatingFile) cleanupLocked(maxFile, maxTotal int64) {
	budget := maxTotal - maxFile
	if budget < 0 {
		budget = 0
	}
	for {
		var total int64
		oldest := 0
		for i := 1; ; i++ {
			info, err := os.Stat(backupPath(f.path, i))
			if err != nil {
				oldest = i - 1
				break
			}
			total += info.Size()
		}
		if total <= budget || oldest == 0 {
			return
		}
		if os.Remove(backupPath(f.path, oldest)) != nil {
			return
		}
	}
}

func (f *RotatingFile) closeLocked() {
	if f.file != nil {
		f.file.Close()
		f.file = nil
		f.size = 0
	}
}

func backupPath(path string, n int) string {
	return path + "." + strconv.Itoa(n)
}
