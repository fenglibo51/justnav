package wallpaper

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var dateRe = regexp.MustCompile(`^(\d{8})\.(jpg|jpeg|png|webp)$`)

type cacheEntry struct {
	date string
	name string
	size int64
}

// Cleanup 按「保留天数」和「总容量」两级清理，永不删当前正在使用的那张。
//
// 顺序很重要：先按日期淘汰过期文件，再按总容量从最旧的开始删。
// 反过来的话，一次容量清理可能把当天那张也删掉。
func (m *Manager) Cleanup(keepDays int, maxBytes int64) (int, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var items []cacheEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		mt := dateRe.FindStringSubmatch(e.Name())
		if mt == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, cacheEntry{date: mt[1], name: e.Name(), size: info.Size()})
	}
	// 新的在前
	sort.Slice(items, func(i, j int) bool { return items[i].date > items[j].date })

	cur, hasCur := m.Current()
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("20060102")

	removed := 0
	var kept []cacheEntry
	for _, it := range items {
		// 当前使用中的绝不删
		if hasCur && it.date == cur.Date {
			kept = append(kept, it)
			continue
		}
		if it.date < cutoff {
			if err := os.Remove(filepath.Join(m.dir, it.name)); err == nil {
				removed++
			}
			continue
		}
		kept = append(kept, it)
	}

	// 容量上限：超了就从最旧的开始删
	var total int64
	for _, it := range kept {
		total += it.size
	}
	for i := len(kept) - 1; i >= 0 && total > maxBytes; i-- {
		it := kept[i]
		if hasCur && it.date == cur.Date {
			continue
		}
		if err := os.Remove(filepath.Join(m.dir, it.name)); err == nil {
			total -= it.size
			removed++
		}
	}
	return removed, nil
}

// Cached 返回缓存中的壁纸列表（新的在前），供前端做「最近几天」选择。
func (m *Manager) Cached() []Meta {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var out []Meta
	for _, e := range entries {
		mt := dateRe.FindStringSubmatch(e.Name())
		if mt == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Meta{
			Date:  mt[1],
			File:  e.Name(),
			Bytes: info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out
}

// sweepTempFiles 清掉进程被 kill 时留下的临时文件。
func (m *Manager) sweepTempFiles() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".meta-") && strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(m.dir, e.Name()))
		}
	}
}
