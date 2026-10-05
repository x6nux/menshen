package logbuf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLogPathFor：日志文件与数据库同目录、同前缀（data.db → data.log），
// 运维按 db_path 就能推出来它在哪。
func TestLogPathFor(t *testing.T) {
	cases := []struct{ db, want string }{
		{"data.db", "data.log"},
		{"/app/data/data.db", "/app/data/data.log"},
		{"/app/data/menshen.sqlite", "/app/data/menshen.log"},
		// 无扩展名直接追加；多点文件名只去最后一个扩展名。
		{"/app/data/menshen", "/app/data/menshen.log"},
		{"/app/data/my.db.v2.db", "/app/data/my.db.v2.log"},
	}
	for _, c := range cases {
		if got := LogPathFor(c.db); got != c.want {
			t.Errorf("LogPathFor(%q) = %q，期望 %q", c.db, got, c.want)
		}
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func filesIn(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func totalSize(t *testing.T, paths []string) int64 {
	t.Helper()
	var n int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		n += info.Size()
	}
	return n
}

// TestRotatingFileRotatesOnSize：单文件写满就整体挪成 .1，此前的备份
// 依次上移，写入不中断也不丢已写内容。
func TestRotatingFileRotatesOnSize(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "data.log")
	// 两行各 30 字节，单文件上限 40：第二行必须滚进新文件。
	line := strings.Repeat("a", 29) + "\n"
	maxFile, maxTotal := int64(40), int64(1<<20)
	f := NewRotatingFile(logPath, func() (int64, int64) { return maxFile, maxTotal })
	defer f.Close()

	if _, err := f.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	backup := filepath.Join(dir, "data.log.1")
	first, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("第一行应滚进备份 .1：%v", err)
	}
	if string(first) != line {
		t.Errorf("备份内容不对：%q", first)
	}
	cur, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("当前文件应存在：%v", err)
	}
	if string(cur) != line {
		t.Errorf("当前文件应只有第二行：%q", cur)
	}
}

// TestRotatingFileEnforcesTotal：总上限按「备份 + 给当前文件留一个单文件
// 的余量」执行 —— 当前文件永远 ≤ 单文件上限，于是落盘总量不超总上限。
func TestRotatingFileEnforcesTotal(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "data.log")
	line := strings.Repeat("b", 29) + "\n" // 30 字节
	maxFile, maxTotal := int64(30), int64(100)
	f := NewRotatingFile(logPath, func() (int64, int64) { return maxFile, maxTotal })
	defer f.Close()

	for i := 0; i < 20; i++ {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	files := filesIn(t, dir, "data.log")
	// 余量 100-30=70 → 备份最多 2 个，加当前文件共 3 个。
	if len(files) > 3 {
		t.Errorf("总上限 100 应只留 3 个文件，得到 %d 个：%v", len(files), files)
	}
	if size := totalSize(t, files); size > maxTotal {
		t.Errorf("落盘总量应 ≤ %d 字节，得到 %d", maxTotal, size)
	}
	// 最旧的被删、最新的还在：当前文件是最后一行，.1 是倒数第二行。
	cur, err := os.ReadFile(logPath)
	if err != nil || string(cur) != line {
		t.Errorf("当前文件应是最后写入的一行：%q err=%v", cur, err)
	}
}

// TestRotatingFileAppliesShrunkLimit：运行中把总上限调小，下一次写入就
// 按新上限清理备份 —— 关键场景是这次写入**不触发滚动**（当前文件还没满），
// 靠滚动兜底的话超量的备份会一直留着。
func TestRotatingFileAppliesShrunkLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "data.log")
	line := strings.Repeat("c", 99) + "\n" // 100 字节
	maxFile, maxTotal := int64(1000), int64(1<<20)
	f := NewRotatingFile(logPath, func() (int64, int64) { return maxFile, maxTotal })
	defer f.Close()

	for i := 0; i < 25; i++ {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	// 25 行 = 备份 .1、.2 各 1000 字节 + 当前文件 500 字节。
	if got := len(filesIn(t, dir, "data.log")); got != 3 {
		t.Fatalf("宽上限下应有 3 个文件，得到 %d 个：%v", got, filesIn(t, dir, "data.log"))
	}

	maxTotal = 1200 // 调小：预算 1200-1000=200 → 备份全部删掉
	// 这一行让当前文件到 600，仍不触发滚动 —— 清理只能来自「上限改动」分支。
	if _, err := f.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	files := filesIn(t, dir, "data.log")
	if size := totalSize(t, files); size > maxTotal {
		t.Errorf("调小后落盘总量应 ≤ %d 字节，得到 %d（%v）", maxTotal, size, files)
	}
}

// TestRotatingFileDisabledWhenZero：任一上限为 0 = 关闭文件日志，
// 不创建文件也不报错。
func TestRotatingFileDisabledWhenZero(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "data.log")

	for _, limits := range [][2]int64{{0, 50}, {10, 0}} {
		f := NewRotatingFile(logPath, func() (int64, int64) { return limits[0], limits[1] })
		if _, err := f.Write([]byte("这行不该落盘\n")); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("关闭状态下不应创建日志文件，stat err=%v", err)
	}
}

// TestRotatingFileAppendsAcrossReopen：同一个写入器关了再写（上限从 0
// 打开），续写而不是截断——重启后旧日志还在。
func TestRotatingFileAppendsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "data.log")
	maxFile, maxTotal := int64(1<<20), int64(1<<20)
	off := false
	f := NewRotatingFile(logPath, func() (int64, int64) {
		if off {
			return 0, 0
		}
		return maxFile, maxTotal
	})
	defer f.Close()

	if _, err := f.Write([]byte("第一行\n")); err != nil {
		t.Fatal(err)
	}
	off = true
	if _, err := f.Write([]byte("关闭期丢弃\n")); err != nil {
		t.Fatal(err)
	}
	off = false
	if _, err := f.Write([]byte("第二行\n")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "第一行\n第二行\n" {
		t.Errorf("应续写且关闭期的行不落盘，得到 %q", data)
	}
}
