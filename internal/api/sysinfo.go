package api

import (
	"bufio"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HostStats 宿主机（NAS）实时资源占用：CPU / 内存 / 存储池。
// 容器内 /proc/stat 与 /proc/meminfo 直接反映宿主机内核数据，
// 对 s.Cfg.DataDir (/data) 做 Statfs 即为宿主机 /volume1 存储池容量。
type HostStats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    uint64  `json:"memUsed"`
	MemTotal   uint64  `json:"memTotal"`
	MemPercent float64 `json:"memPercent"`
	DiskUsed   uint64  `json:"diskUsed"`
	DiskTotal  uint64  `json:"diskTotal"`
	DiskPct    float64 `json:"diskPercent"`
}

var (
	cpuMu       sync.Mutex
	lastCPUIdle uint64
	lastCPUTot  uint64
	lastCPUPct  float64
	lastCPUAt   time.Time
)

func readCPUTicks() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, false
		}
		for i := 1; i < len(fields); i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				continue
			}
			total += v
			if i == 4 || i == 5 { // idle + iowait
				idle += v
			}
		}
		return idle, total, true
	}
	return 0, 0, false
}

func sampleCPUPercent() float64 {
	cpuMu.Lock()
	defer cpuMu.Unlock()

	idle1, tot1, ok := readCPUTicks()
	if !ok {
		return 0
	}
	// 首次或间隔太久：短采样 120ms 拿实时差值
	if lastCPUTot == 0 || time.Since(lastCPUAt) > 2*time.Minute {
		lastCPUIdle, lastCPUTot, lastCPUAt = idle1, tot1, time.Now()
		time.Sleep(120 * time.Millisecond)
		idle2, tot2, ok2 := readCPUTicks()
		if !ok2 || tot2 <= tot1 {
			return lastCPUPct
		}
		dIdle := float64(idle2 - idle1)
		dTot := float64(tot2 - tot1)
		lastCPUIdle, lastCPUTot, lastCPUAt = idle2, tot2, time.Now()
		lastCPUPct = clampPct((1.0 - dIdle/dTot) * 100.0)
		return lastCPUPct
	}
	if tot1 > lastCPUTot {
		dIdle := float64(idle1 - lastCPUIdle)
		dTot := float64(tot1 - lastCPUTot)
		lastCPUIdle, lastCPUTot, lastCPUAt = idle1, tot1, time.Now()
		lastCPUPct = clampPct((1.0 - dIdle/dTot) * 100.0)
	}
	return lastCPUPct
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return float64(int(v*10+0.5)) / 10.0
}

func readMemStats() (used, total uint64, pct float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	var memTotalKB, memAvailKB, memFreeKB, buffersKB, cachedKB uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 2 {
			continue
		}
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(parts[0], ":") {
		case "MemTotal":
			memTotalKB = val
		case "MemAvailable":
			memAvailKB = val
		case "MemFree":
			memFreeKB = val
		case "Buffers":
			buffersKB = val
		case "Cached":
			cachedKB = val
		}
	}
	if memTotalKB == 0 {
		return 0, 0, 0
	}
	if memAvailKB == 0 {
		memAvailKB = memFreeKB + buffersKB + cachedKB
	}
	total = memTotalKB * 1024
	if memTotalKB > memAvailKB {
		used = (memTotalKB - memAvailKB) * 1024
	}
	pct = clampPct(float64(used) / float64(total) * 100.0)
	return used, total, pct
}

func readDiskStats(path string) (used, total uint64, pct float64) {
	if path == "" {
		path = "."
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		if err2 := syscall.Statfs("/", &st); err2 != nil {
			return 0, 0, 0
		}
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	avail := st.Bavail * bsize
	if total > avail {
		used = total - avail
	}
	if total > 0 {
		pct = clampPct(float64(used) / float64(total) * 100.0)
	}
	return used, total, pct
}

func (s *Server) collectHostStats() HostStats {
	cpu := sampleCPUPercent()
	mu, mt, mp := readMemStats()
	du, dt, dp := readDiskStats(s.Cfg.DataDir)
	return HostStats{
		CPUPercent: cpu,
		MemUsed:    mu,
		MemTotal:   mt,
		MemPercent: mp,
		DiskUsed:   du,
		DiskTotal:  dt,
		DiskPct:    dp,
	}
}

func (s *Server) handleSysinfo(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.storeFor(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"host": s.collectHostStats(),
	})
}
