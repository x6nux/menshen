package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSnapshotLocation：展示时区优先取 tz_name（IANA 名称）；没配或
// 配错的部署要回落到旧的 tz_offset 小时偏移——升级不能改变现有行为。
func TestSnapshotLocation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := NewCache(s)
	if err != nil {
		t.Fatal(err)
	}
	snap := c.Snap()

	// 默认 tz_name=Asia/Shanghai：与旧默认 tz_offset=8 同为 UTC+8。
	if got := snap.Location().String(); got != "Asia/Shanghai" {
		t.Errorf("默认时区应为 Asia/Shanghai，得到 %s", got)
	}
	utc8 := time.Unix(1700000000, 0).In(snap.Location()).Format("-07:00")
	if utc8 != "+08:00" {
		t.Errorf("默认时区的偏移应为 +08:00，得到 %s", utc8)
	}

	// 配了合法 IANA 名就按它来。
	put := func(k, v string) {
		if _, err := s.Write.Exec(`INSERT INTO settings (k,v) VALUES (?,?)
			ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v); err != nil {
			t.Fatal(err)
		}
		if err := c.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	put("tz_name", "Europe/London")
	if got := c.Snap().Location().String(); got != "Europe/London" {
		t.Errorf("应使用 tz_name，得到 %s", got)
	}

	// 配错了回落 tz_offset；tz_offset 本身也不再是默认值 8。
	put("tz_name", "Not/AZone")
	put("tz_offset", "-3")
	off := time.Unix(1700000000, 0).In(c.Snap().Location()).Format("-07:00")
	if off != "-03:00" {
		t.Errorf("非法 tz_name 应回落 tz_offset=-3，得到 %s", off)
	}
}
