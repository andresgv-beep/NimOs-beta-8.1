package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ═══════════════════════════════════
// Startup detection
// ═══════════════════════════════════

var (
	hasSmartctl bool
	hasSensors  bool
	hasDocker   bool
	hasNvidia   bool
	hasAmdDrm   bool
	systemArch  string
	systemRamGB int
)

// Pre-compiled regexes for hot paths (avoid recompiling in loops)
var (
	reSdDisk   = regexp.MustCompile(`^sd[a-z]+$`)
	reNvmeDisk = regexp.MustCompile(`^nvme\d+n\d+$`)
	reVdDisk   = regexp.MustCompile(`^vd[a-z]+$`)
)

func detectHardwareTools() {
	_, hasSmartctl = runSafe("which", "smartctl")
	_, hasSensors = runSafe("which", "sensors")
	_, hasDocker = runSafe("which", "docker")
	_, hasNvidia = runSafe("which", "nvidia-smi")
	hasAmdDrm = detectAmdDrm()

	// Beta 8: ZFS no longer supported. Only BTRFS is detected.
	detectBtrfs()

	// System info
	archOut, _ := runSafe("uname", "-m")
	systemArch = strings.TrimSpace(archOut)

	// RAM: leer /proc/meminfo directamente sin shell.
	// Antes usábamos runShellStatic con un pipe awk, pero el shield lo
	// rechaza por interpolar comandos. Leer el archivo nativo es más
	// seguro, más rápido, y no dispara warnings de seguridad.
	if meminfoBytes, err := os.ReadFile("/proc/meminfo"); err == nil {
		re := regexp.MustCompile(`MemTotal:\s+(\d+)\s+kB`)
		if m := re.FindStringSubmatch(string(meminfoBytes)); m != nil {
			kb := parseInt64(m[1])
			systemRamGB = int(kb / 1024 / 1024) // kB → GB
		}
	}

	if hasBtrfs {
		logMsg("Btrfs available (arch=%s, ram=%dGB)", systemArch, systemRamGB)
	} else {
		logMsg("WARNING: No supported storage backend (arch=%s, ram=%dGB) — install btrfs-progs", systemArch, systemRamGB)
	}
}

func detectAmdDrm() bool {
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if matched, _ := regexp.MatchString(`^card\d$`, e.Name()); matched {
			if data := readFileStr(fmt.Sprintf("/sys/class/drm/%s/device/gpu_busy_percent", e.Name())); data != "" {
				return true
			}
		}
	}
	return false
}

// ═══════════════════════════════════
// Helpers
// ═══════════════════════════════════

func readFileStr(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	sizes := []string{"B", "KB", "MB", "GB", "TB"}
	i := int(math.Floor(math.Log(math.Abs(float64(bytes))) / math.Log(1024)))
	if i >= len(sizes) {
		i = len(sizes) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(bytes)/math.Pow(1024, float64(i)), sizes[i])
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func parseIntDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// ═══════════════════════════════════
// CPU
// ═══════════════════════════════════

var prevCpuIdle, prevCpuTotal int64

// ── Disk I/O tracking (for /api/hardware/stats) ──
var (
	prevDiskRead    int64
	prevDiskWrite   int64
	prevDiskTime    int64
)

// getDiskIO reads /proc/diskstats and returns aggregate read/write bytes per second.
// Only counts whole-disk devices (sda, nvme0n1, vda), not partitions.
func getDiskIO() map[string]interface{} {
	data := readFileStr("/proc/diskstats")
	if data == "" {
		return map[string]interface{}{"readSpeed": 0, "writeSpeed": 0}
	}

	var totalRead, totalWrite int64
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		// Only whole disks, not partitions
		// sd[a-z], nvme[0-9]n[0-9], vd[a-z], xvd[a-z]
		isWholeDisk := false
		if reSdDisk.MatchString(dev) ||
			reNvmeDisk.MatchString(dev) ||
			reVdDisk.MatchString(dev) {
			isWholeDisk = true
		}
		if !isWholeDisk {
			continue
		}
		// fields[5] = sectors read, fields[9] = sectors written
		// Sector size = 512 bytes
		totalRead += parseInt64(fields[5]) * 512
		totalWrite += parseInt64(fields[9]) * 512
	}

	now := time.Now().UnixMilli()
	var readSpeed, writeSpeed int64
	if prevDiskTime > 0 {
		dt := float64(now-prevDiskTime) / 1000
		if dt > 0 {
			readSpeed = int64(math.Round(float64(totalRead-prevDiskRead) / dt))
			writeSpeed = int64(math.Round(float64(totalWrite-prevDiskWrite) / dt))
			if readSpeed < 0 { readSpeed = 0 }
			if writeSpeed < 0 { writeSpeed = 0 }
		}
	}
	prevDiskRead = totalRead
	prevDiskWrite = totalWrite
	prevDiskTime = now

	return map[string]interface{}{
		"readSpeed":  readSpeed,
		"writeSpeed": writeSpeed,
	}
}

// getNetworkAggregate returns total rx/tx bytes per second across all physical interfaces.
// Uses its own tracking vars to avoid interfering with getNetwork() per-interface stats.
var (
	prevNetAgg   = map[string]netStat{}
	prevNetAggMu sync.Mutex
)

func getNetworkAggregate() map[string]interface{} {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return map[string]interface{}{"rxSpeed": 0, "txSpeed": 0}
	}

	var totalRx, totalTx int64
	now := time.Now().UnixMilli()

	prevNetAggMu.Lock()
	defer prevNetAggMu.Unlock()

	for _, e := range entries {
		dev := e.Name()
		if !isPhysicalInterface(dev) {
			continue
		}
		operstate := readFileStr(fmt.Sprintf("/sys/class/net/%s/operstate", dev))
		if operstate != "up" {
			continue
		}
		rxBytes := parseInt64(readFileStr(fmt.Sprintf("/sys/class/net/%s/statistics/rx_bytes", dev)))
		txBytes := parseInt64(readFileStr(fmt.Sprintf("/sys/class/net/%s/statistics/tx_bytes", dev)))

		if prev, ok := prevNetAgg[dev]; ok {
			dt := float64(now-prev.time) / 1000
			if dt > 0 {
				totalRx += int64(math.Round(float64(rxBytes-prev.rx) / dt))
				totalTx += int64(math.Round(float64(txBytes-prev.tx) / dt))
			}
		}
		prevNetAgg[dev] = netStat{rx: rxBytes, tx: txBytes, time: now}
	}

	if totalRx < 0 { totalRx = 0 }
	if totalTx < 0 { totalTx = 0 }

	return map[string]interface{}{
		"rxSpeed": totalRx,
		"txSpeed": totalTx,
	}
}

func getCpuUsage() map[string]interface{} {
	stat := readFileStr("/proc/stat")
	cpuCount := 0
	cpuModel := "Unknown"
	cpuInfo := readFileStr("/proc/cpuinfo")
	if cpuInfo != "" {
		for _, line := range strings.Split(cpuInfo, "\n") {
			if strings.HasPrefix(line, "processor") {
				cpuCount++
			}
			if strings.HasPrefix(line, "model name") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					cpuModel = strings.TrimSpace(parts[1])
				}
			}
		}
		// ARM: no "model name" — try /proc/device-tree/model or Hardware line
		if cpuModel == "Unknown" {
			if dtModel := readFileStr("/proc/device-tree/model"); dtModel != "" {
				cpuModel = strings.TrimRight(dtModel, "\x00\n")
			} else {
				for _, line := range strings.Split(cpuInfo, "\n") {
					if strings.HasPrefix(line, "Hardware") {
						parts := strings.SplitN(line, ":", 2)
						if len(parts) == 2 {
							cpuModel = strings.TrimSpace(parts[1])
						}
						break
					}
				}
			}
		}
	}
	if cpuCount == 0 {
		cpuCount = 1
	}

	percent := 0
	if stat != "" {
		line := strings.Split(stat, "\n")[0]
		fields := strings.Fields(line)
		if len(fields) >= 8 {
			var values []int64
			for _, f := range fields[1:] {
				values = append(values, parseInt64(f))
			}
			idle := values[3]
			if len(values) > 4 {
				idle += values[4] // iowait
			}
			total := int64(0)
			for _, v := range values {
				total += v
			}

			if prevCpuTotal > 0 {
				diffIdle := idle - prevCpuIdle
				diffTotal := total - prevCpuTotal
				if diffTotal > 0 {
					percent = int(math.Round(float64(diffTotal-diffIdle) / float64(diffTotal) * 100))
				}
			}
			prevCpuIdle = idle
			prevCpuTotal = total
		}
	}

	return map[string]interface{}{
		"percent": percent,
		"cores":   cpuCount,
		"model":   cpuModel,
	}
}

// ═══════════════════════════════════
// Memory
// ═══════════════════════════════════

func getMemory() map[string]interface{} {
	info := readFileStr("/proc/meminfo")
	if info == "" {
		return map[string]interface{}{"total": 0, "used": 0, "percent": 0}
	}

	parse := func(key string) int64 {
		re := regexp.MustCompile(key + `:\s+(\d+)`)
		m := re.FindStringSubmatch(info)
		if m == nil {
			return 0
		}
		return parseInt64(m[1]) * 1024 // kB to bytes
	}

	total := parse("MemTotal")
	available := parse("MemAvailable")
	used := total - available

	return map[string]interface{}{
		"total":   total,
		"used":    used,
		"totalGB": fmt.Sprintf("%.1f", float64(total)/1073741824),
		"usedGB":  fmt.Sprintf("%.1f", float64(used)/1073741824),
		"percent": func() int {
			if total > 0 {
				return int(math.Round(float64(used) / float64(total) * 100))
			}
			return 0
		}(),
	}
}

// ═══════════════════════════════════
// GPU
// ═══════════════════════════════════

func getGpu() []map[string]interface{} {
	var gpus []map[string]interface{}

	if hasNvidia {
		out, ok := runSafe("nvidia-smi", "--query-gpu=index,name,utilization.gpu,temperature.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
		if ok && out != "" {
			for _, line := range strings.Split(out, "\n") {
				parts := strings.Split(line, ",")
				if len(parts) >= 6 {
					memUsed := parseIntDefault(strings.TrimSpace(parts[4]), 0)
					memTotal := parseIntDefault(strings.TrimSpace(parts[5]), 0)
					memPct := 0
					if memTotal > 0 {
						memPct = int(math.Round(float64(memUsed) / float64(memTotal) * 100))
					}
					gpus = append(gpus, map[string]interface{}{
						"index":       parseIntDefault(strings.TrimSpace(parts[0]), 0),
						"name":        strings.TrimSpace(parts[1]),
						"utilization": parseIntDefault(strings.TrimSpace(parts[2]), 0),
						"temperature": parseIntDefault(strings.TrimSpace(parts[3]), 0),
						"memUsed":     memUsed,
						"memTotal":    memTotal,
						"memPercent":  memPct,
						"driver":      "nvidia",
					})
				}
			}
		}
	}

	if hasAmdDrm {
		entries, _ := os.ReadDir("/sys/class/drm")
		for _, e := range entries {
			if matched, _ := regexp.MatchString(`^card\d$`, e.Name()); !matched {
				continue
			}
			busy := readFileStr(fmt.Sprintf("/sys/class/drm/%s/device/gpu_busy_percent", e.Name()))
			if busy == "" {
				continue
			}
			// Find temperature
			temp := 0
			hwmonDirs, _ := filepath.Glob(fmt.Sprintf("/sys/class/drm/%s/device/hwmon/hwmon*", e.Name()))
			for _, dir := range hwmonDirs {
				t := readFileStr(filepath.Join(dir, "temp1_input"))
				if t != "" {
					temp = parseIntDefault(t, 0) / 1000
					break
				}
			}
			gpus = append(gpus, map[string]interface{}{
				"index":       len(gpus),
				"name":        fmt.Sprintf("AMD GPU (%s)", e.Name()),
				"utilization": parseIntDefault(busy, 0),
				"temperature": temp,
				"memUsed":     0,
				"memTotal":    0,
				"memPercent":  0,
				"driver":      "amd",
			})
		}
	}

	if gpus == nil {
		gpus = []map[string]interface{}{}
	}
	return gpus
}

// ═══════════════════════════════════
// GPU Driver Info
// ═══════════════════════════════════

func getHardwareGpuInfo() map[string]interface{} {
	result := map[string]interface{}{
		"gpus":             []interface{}{},
		"currentDriver":    nil,
		"driverVersion":    nil,
		"availableDrivers": []interface{}{},
		"kernelModules":    []interface{}{},
	}

	// Detect GPUs via lspci
	var gpuList []interface{}
	lspci, ok := runShellStatic(`lspci -nn 2>/dev/null | grep -iE "VGA|3D|Display"`)
	if ok && lspci != "" {
		for _, line := range strings.Split(lspci, "\n") {
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			vendor := "unknown"
			if strings.Contains(lower, "nvidia") {
				vendor = "nvidia"
			} else if strings.Contains(lower, "amd") || strings.Contains(lower, "ati") {
				vendor = "amd"
			} else if strings.Contains(lower, "intel") {
				vendor = "intel"
			}
			pciId := ""
			if m := regexp.MustCompile(`\[([0-9a-f]{4}:[0-9a-f]{4})\]`).FindStringSubmatch(line); m != nil {
				pciId = m[1]
			}
			desc := line
			if idx := strings.Index(line, " "); idx > 0 {
				desc = strings.TrimSpace(line[idx:])
			}
			gpuList = append(gpuList, map[string]interface{}{
				"description": desc,
				"vendor":      vendor,
				"pciId":       pciId,
			})
		}
	}

	// ARM fallback
	if len(gpuList) == 0 {
		if vcgencmd, ok := runSafe("vcgencmd", "get_mem", "gpu"); ok && vcgencmd != "" {
			model := readFileStr("/proc/device-tree/model")
			if model == "" {
				model = "Raspberry Pi"
			}
			gpuMem := strings.Replace(strings.Replace(vcgencmd, "gpu=", "", 1), "M", " MB", 1)
			gpuList = append(gpuList, map[string]interface{}{
				"description": fmt.Sprintf("%s — VideoCore (%s)", strings.TrimSpace(model), strings.TrimSpace(gpuMem)),
				"vendor":      "broadcom",
				"pciId":       nil,
			})
			result["currentDriver"] = "v3d"
		}
	}
	if gpuList == nil {
		gpuList = []interface{}{}
	}
	result["gpus"] = gpuList

	// NVIDIA driver
	if hasNvidia {
		if ver, ok := runSafe("nvidia-smi", "--query-gpu=driver_version", "--format=csv,noheader,nounits"); ok && ver != "" {
			result["currentDriver"] = "nvidia"
			result["driverVersion"] = strings.TrimSpace(strings.Split(ver, "\n")[0])
		}
	}

	// AMD driver
	if out, ok := runShellStatic("lsmod 2>/dev/null | grep amdgpu"); ok && out != "" {
		if result["currentDriver"] == nil {
			result["currentDriver"] = "amdgpu"
		}
	}

	// Intel driver
	if out, ok := runShellStatic("lsmod 2>/dev/null | grep i915"); ok && out != "" {
		if result["currentDriver"] == nil {
			result["currentDriver"] = "i915"
		}
	}

	// Kernel modules
	var modules []interface{}
	if mods, ok := runShellStatic(`lsmod 2>/dev/null | grep -iE "nvidia|amdgpu|radeon|i915|nouveau"`); ok && mods != "" {
		for _, line := range strings.Split(mods, "\n") {
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			entry := map[string]interface{}{"name": parts[0]}
			if len(parts) > 1 {
				entry["size"] = parts[1]
			}
			if len(parts) > 3 {
				entry["usedBy"] = parts[3]
			}
			modules = append(modules, entry)
		}
	}
	if modules == nil {
		modules = []interface{}{}
	}
	result["kernelModules"] = modules

	return result
}

// ═══════════════════════════════════
// Temperatures
// ═══════════════════════════════════

func getTemps(gpusCache []map[string]interface{}) map[string]interface{} {
	temps := map[string]interface{}{}

	// CPU via /sys/class/thermal
	entries, _ := os.ReadDir("/sys/class/thermal")
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		typeName := readFileStr(fmt.Sprintf("/sys/class/thermal/%s/type", e.Name()))
		tempStr := readFileStr(fmt.Sprintf("/sys/class/thermal/%s/temp", e.Name()))
		if typeName != "" && tempStr != "" {
			temps[typeName] = parseIntDefault(tempStr, 0) / 1000
		}
	}

	// lm-sensors fallback
	if len(temps) == 0 && hasSensors {
		if out, ok := runSafe("sensors", "-u"); ok {
			re := regexp.MustCompile(`temp1_input:\s+([\d.]+)`)
			if m := re.FindStringSubmatch(out); m != nil {
				temps["cpu"] = int(math.Round(parseFloat(m[1])))
			}
		}
	}

	// GPU temps
	gpus := gpusCache
	if gpus == nil {
		gpus = getGpu()
	}
	for i, g := range gpus {
		if t, ok := g["temperature"].(int); ok && t > 0 {
			temps[fmt.Sprintf("gpu%d", i)] = t
		}
	}

	return temps
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// ═══════════════════════════════════
// Network
// ═══════════════════════════════════

var (
	prevNetStats   = map[string]netStat{}
	prevNetStatsMu sync.Mutex
)

type netStat struct {
	rx, tx int64
	time   int64
}

func isPhysicalInterface(dev string) bool {
	skip := []string{"lo", "docker", "br-", "veth", "virbr", "tun", "tap"}
	for _, s := range skip {
		if dev == s || strings.HasPrefix(dev, s) {
			return false
		}
	}
	// Check physical device
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s/device", dev)); err == nil {
		return true
	}
	// Allow common naming patterns
	for _, prefix := range []string{"eth", "enp", "eno", "ens", "wl"} {
		if strings.HasPrefix(dev, prefix) {
			return true
		}
	}
	return false
}

func getNetwork() []map[string]interface{} {
	var interfaces []map[string]interface{}

	// Get all IPs
	allIps := map[string]string{}
	if ipOut, ok := runSafe("ip", "-4", "-o", "addr", "show"); ok {
		for _, line := range strings.Split(ipOut, "\n") {
			re := regexp.MustCompile(`^\d+:\s+(\S+)\s+inet\s+([\d.]+)`)
			if m := re.FindStringSubmatch(line); m != nil {
				allIps[m[1]] = m[2]
			}
		}
	}

	entries, _ := os.ReadDir("/sys/class/net")
	prevNetStatsMu.Lock()
	defer prevNetStatsMu.Unlock()

	now := time.Now().UnixMilli()

	for _, e := range entries {
		dev := e.Name()
		if !isPhysicalInterface(dev) {
			continue
		}

		operstate := readFileStr(fmt.Sprintf("/sys/class/net/%s/operstate", dev))
		if operstate != "up" {
			continue
		}

		speed := readFileStr(fmt.Sprintf("/sys/class/net/%s/speed", dev))
		rxBytes := parseInt64(readFileStr(fmt.Sprintf("/sys/class/net/%s/statistics/rx_bytes", dev)))
		txBytes := parseInt64(readFileStr(fmt.Sprintf("/sys/class/net/%s/statistics/tx_bytes", dev)))
		mac := readFileStr(fmt.Sprintf("/sys/class/net/%s/address", dev))
		isWifi := strings.HasPrefix(dev, "wl")

		var ssid, signal interface{}
		ssid = nil
		signal = nil
		if isWifi {
			if s, ok := runSafe("iwgetid", "-r", dev); ok && s != "" {
				ssid = strings.TrimSpace(s)
			}
			if sig, ok := runSafe("iwconfig", dev); ok {
				re := regexp.MustCompile(`Signal level[=:]?\s*(-?\d+)`)
				if m := re.FindStringSubmatch(sig); m != nil {
					signal = parseIntDefault(m[1], 0)
				}
			}
		}

		// Calculate rates
		var rxRate, txRate int64
		if prev, ok := prevNetStats[dev]; ok {
			dt := float64(now-prev.time) / 1000
			if dt > 0 {
				rxRate = int64(math.Round(float64(rxBytes-prev.rx) / dt))
				txRate = int64(math.Round(float64(txBytes-prev.tx) / dt))
			}
		}
		prevNetStats[dev] = netStat{rx: rxBytes, tx: txBytes, time: now}

		speedStr := "—"
		if speed != "" {
			n := parseIntDefault(speed, 0)
			if n > 0 {
				speedStr = fmt.Sprintf("%s Mbps", speed)
			} else if isWifi && ssid != nil {
				speedStr = "WiFi"
			}
		}

		iface := map[string]interface{}{
			"name":            dev,
			"type":            "ethernet",
			"status":          operstate,
			"speed":           speedStr,
			"ip":              allIps[dev],
			"mac":             mac,
			"ssid":            ssid,
			"signal":          signal,
			"rxBytes":         rxBytes,
			"txBytes":         txBytes,
			"rxRate":          rxRate,
			"txRate":          txRate,
			"rxRateFormatted": formatBytes(rxRate) + "/s",
			"txRateFormatted": formatBytes(txRate) + "/s",
		}
		if isWifi {
			iface["type"] = "wifi"
		}
		if _, ok := allIps[dev]; !ok {
			iface["ip"] = "—"
		}
		interfaces = append(interfaces, iface)
	}

	if interfaces == nil {
		interfaces = []map[string]interface{}{}
	}
	return interfaces
}

// ═══════════════════════════════════
// Disks
// ═══════════════════════════════════

var (
	diskCache     map[string]interface{}
	diskCacheTime int64
	diskCacheMu   sync.Mutex
)

func getDisks() map[string]interface{} {
	diskCacheMu.Lock()
	defer diskCacheMu.Unlock()

	now := time.Now().UnixMilli()

	// Cache hardware info for 60s
	if diskCache == nil || (now-diskCacheTime) > 60000 {
		var disks []interface{}
		if lsblk, ok := runSafe("lsblk", "-Jbdo", "NAME,SIZE,MODEL,TYPE,TRAN"); ok && lsblk != "" {
			var data struct {
				BlockDevices []struct {
					Name  string `json:"name"`
					Size  string `json:"size"`
					Model string `json:"model"`
					Type  string `json:"type"`
					Tran  string `json:"tran"`
				} `json:"blockdevices"`
			}
			if json.Unmarshal([]byte(lsblk), &data) == nil {
				for _, dev := range data.BlockDevices {
					if dev.Type != "disk" {
						continue
					}
					if strings.HasPrefix(dev.Name, "loop") || strings.HasPrefix(dev.Name, "ram") || strings.HasPrefix(dev.Name, "zram") {
						continue
					}
					size := parseInt64(dev.Size)
					if size <= 0 {
						continue
					}

					var temp interface{}
					if hasSmartctl && isValidDev(dev.Name) {
						if smart, ok := runSafe("smartctl", "-A", "/dev/"+dev.Name); ok && smart != "" {
							// Filter for temperature line
							for _, line := range strings.Split(smart, "\n") {
								if strings.Contains(strings.ToLower(line), "temperature") {
									re := regexp.MustCompile(`(\d+)\s*$`)
									if m := re.FindStringSubmatch(line); m != nil {
										temp = parseIntDefault(m[1], 0)
									}
									break
								}
							}
						}
					}

					tran := dev.Tran
					if tran == "" {
						tran = "—"
					}
					disks = append(disks, map[string]interface{}{
						"name":          fmt.Sprintf("/dev/%s", dev.Name),
						"model":         strings.TrimSpace(dev.Model),
						"size":          size,
						"sizeFormatted": formatBytes(size),
						"temperature":   temp,
						"transport":     tran,
						"type":          "disk",
					})
				}
			}
		}
		if disks == nil {
			disks = []interface{}{}
		}

		// RAID
		var raids []interface{}
		mdstat := readFileStr("/proc/mdstat")
		if mdstat != "" {
			re := regexp.MustCompile(`(?m)^(md\d+)\s*:\s*active\s+(\w+)\s+(.+)`)
			for _, m := range re.FindAllStringSubmatch(mdstat, -1) {
				raids = append(raids, map[string]interface{}{
					"name": m[1], "type": m[2], "devices": strings.TrimSpace(m[3]),
				})
			}
		}
		if raids == nil {
			raids = []interface{}{}
		}

		diskCache = map[string]interface{}{"disks": disks, "raids": raids}
		diskCacheTime = now
	}

	// df always fresh
	var mounts []interface{}
	if df, ok := runSafe("df", "-B1", "--output=source,size,used,avail,target"); ok {
		for _, line := range strings.Split(df, "\n")[1:] {
			parts := strings.Fields(line)
			if len(parts) < 5 || !strings.HasPrefix(parts[0], "/dev/") || strings.Contains(parts[0], "loop") {
				continue
			}
			total := parseInt64(parts[1])
			used := parseInt64(parts[2])
			pct := 0
			if total > 0 {
				pct = int(math.Round(float64(used) / float64(total) * 100))
			}
			mounts = append(mounts, map[string]interface{}{
				"device":         parts[0],
				"total":          total,
				"used":           used,
				"available":      parseInt64(parts[3]),
				"mount":          parts[4],
				"totalFormatted": formatBytes(total),
				"usedFormatted":  formatBytes(used),
				"percent":        pct,
			})
		}
	}
	if mounts == nil {
		mounts = []interface{}{}
	}

	result := map[string]interface{}{}
	for k, v := range diskCache {
		result[k] = v
	}
	result["mounts"] = mounts
	return result
}

// ═══════════════════════════════════
// Uptime
// ═══════════════════════════════════

func getUptime() string {
	raw := readFileStr("/proc/uptime")
	if raw == "" {
		return "—"
	}
	secs := parseFloat(strings.Fields(raw)[0])
	days := int(secs) / 86400
	hours := (int(secs) % 86400) / 3600
	mins := (int(secs) % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// ═══════════════════════════════════
// Containers
// ═══════════════════════════════════

var (
	containerCache     []interface{}
	containerCacheTime int64
	containerCacheMu   sync.Mutex
)

func getContainers() []interface{} {
	if !hasDocker {
		return []interface{}{}
	}
	containerCacheMu.Lock()
	defer containerCacheMu.Unlock()

	now := time.Now().UnixMilli()
	if containerCache != nil && (now-containerCacheTime) < 5000 {
		return containerCache
	}

	raw, ok := runSafe("docker", "ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}|{{.State}}|{{.CreatedAt}}")
	if !ok || raw == "" {
		return []interface{}{}
	}

	var containers []interface{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 7)
		if len(parts) < 6 {
			continue
		}
		ports := "—"
		if len(parts) > 4 && parts[4] != "" {
			ports = parts[4]
		}
		c := map[string]interface{}{
			"id": parts[0], "name": parts[1], "image": parts[2],
			"status": parts[3], "ports": ports, "state": parts[5],
		}
		if len(parts) > 6 {
			c["created"] = parts[6]
		}
		containers = append(containers, c)
	}

	// docker stats
	if stats, ok := runSafe("docker", "stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}"); ok && stats != "" {
		statMap := map[string][3]string{}
		for _, line := range strings.Split(stats, "\n") {
			p := strings.SplitN(line, "|", 4)
			if len(p) >= 4 {
				statMap[p[0]] = [3]string{p[1], p[2], p[3]}
			}
		}
		for _, c := range containers {
			cm := c.(map[string]interface{})
			if s, ok := statMap[cm["name"].(string)]; ok {
				cm["cpu"] = s[0]
				cm["mem"] = s[1]
				cm["memPct"] = s[2]
			} else {
				cm["cpu"] = "—"
				cm["mem"] = "—"
				cm["memPct"] = "—"
			}
		}
	}

	if containers == nil {
		containers = []interface{}{}
	}
	containerCache = containers
	containerCacheTime = now
	return containers
}

func containerAction(id, action string) map[string]interface{} {
	allowed := map[string]bool{"start": true, "stop": true, "restart": true, "pause": true, "unpause": true}
	if !allowed[action] {
		return map[string]interface{}{"error": "Invalid action"}
	}
	// Sanitize
	re := regexp.MustCompile(`[^a-zA-Z0-9_.\-/:]+`)
	safeId := re.ReplaceAllString(id, "")
	if safeId == "" || len(safeId) > 256 || strings.Contains(safeId, "..") {
		return map[string]interface{}{"error": "Invalid container ID"}
	}
	out, _ := runSafe("docker", action, safeId)
	return map[string]interface{}{"ok": true, "action": action, "id": safeId, "output": out}
}

// ═══════════════════════════════════
// System Summary
// ═══════════════════════════════════

var (
	systemCache     map[string]interface{}
	systemCacheTime int64
	systemCacheMu   sync.Mutex
)

func getSystemSummary() map[string]interface{} {
	systemCacheMu.Lock()
	defer systemCacheMu.Unlock()

	now := time.Now().UnixMilli()
	if systemCache != nil && (now-systemCacheTime) < 1500 {
		return systemCache
	}

	cpu := getCpuUsage()
	mem := getMemory()
	gpus := getGpu()
	temps := getTemps(gpus)
	network := getNetwork()
	diskInfo := getDisks()
	uptime := getUptime()

	hostname, _ := os.Hostname()

	// Main temp
	var mainTemp interface{}
	for _, key := range []string{"x86_pkg_temp", "cpu", "coretemp"} {
		if v, ok := temps[key]; ok {
			mainTemp = v
			break
		}
	}
	if mainTemp == nil {
		for _, v := range temps {
			mainTemp = v
			break
		}
	}

	// Primary network interface
	var primaryNet interface{}
	for _, n := range network {
		ip, _ := n["ip"].(string)
		status, _ := n["status"].(string)
		if ip != "—" && status == "up" {
			primaryNet = n
			break
		}
	}
	if primaryNet == nil && len(network) > 0 {
		primaryNet = network[0]
	}

	uname, _ := runSafe("uname", "-sr")

	systemCache = map[string]interface{}{
		"cpu":        cpu,
		"memory":     mem,
		"gpus":       gpus,
		"temps":      temps,
		"mainTemp":   mainTemp,
		"network":    network,
		"primaryNet": primaryNet,
		"disks":      diskInfo,
		"uptime":     uptime,
		"hostname":   hostname,
		"platform":   uname,
	}
	systemCacheTime = now
	return systemCache
}

// ═══════════════════════════════════
// Hardware HTTP routes
// ═══════════════════════════════════

func handleHardwareRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	path := r.URL.Path
	switch path {
	case "/api/hardware/stats":
		// Combined stats for NimHealth dashboard
		cpuData := getCpuUsage()
		memData := getMemory()
		diskData := getDiskIO()
		netData := getNetworkAggregate()
		loadStr := ""
		if loadAvg := readFileStr("/proc/loadavg"); loadAvg != "" {
			parts := strings.Fields(loadAvg)
			if len(parts) > 0 {
				loadStr = parts[0]
			}
		}
		cpuData["load1"] = parseFloat(loadStr)
		jsonOk(w, map[string]interface{}{
			"cpu":     cpuData,
			"memory":  memData,
			"disk":    diskData,
			"network": netData,
		})
	case "/api/system":
		jsonOk(w, getSystemSummary())
	case "/api/cpu":
		jsonOk(w, getCpuUsage())
	case "/api/memory":
		jsonOk(w, getMemory())
	case "/api/gpu":
		jsonOk(w, getGpu())
	case "/api/temps":
		jsonOk(w, getTemps(nil))
	case "/api/network":
		jsonOk(w, getNetwork())
	case "/api/disks":
		jsonOk(w, getDisks())
	case "/api/disks/smart":
		disk := r.URL.Query().Get("disk")
		if disk == "" {
			jsonError(w, 400, "Provide disk name (e.g. ?disk=sda)")
			return
		}
		jsonOk(w, getDiskSmart(disk))
	case "/api/disks/smart/summary":
		jsonOk(w, getSmartSummary())
	case "/api/uptime":
		jsonOk(w, map[string]interface{}{"uptime": getUptime()})
	case "/api/containers":
		jsonOk(w, getContainers())
	case "/api/hostname":
		h, _ := os.Hostname()
		jsonOk(w, map[string]interface{}{"hostname": h})
	case "/api/hardware/gpu-info":
		jsonOk(w, getHardwareGpuInfo())
	case "/api/system/info":
		handleSystemInfo(w)
	case "/api/system/update/check":
		handleUpdateCheck(w)
	case "/api/system/update/status":
		handleUpdateStatus(w)
	case "/api/system/reboot", "/api/system/shutdown", "/api/system/reboot-service", "/api/system/update/apply", "/api/terminal":
		// These are POST-only admin routes — reject GET and non-admin
		if r.Method != "POST" {
			jsonError(w, 405, "Method not allowed")
			return
		}
		handleSystemPost(w, r, session)
	default:
		// POST routes need body
		if r.Method == "POST" {
			handleSystemPost(w, r, session)
			return
		}
		jsonError(w, 404, "Not found")
	}
}

func handleSystemPost(w http.ResponseWriter, r *http.Request, session *DBSession) {
	if session.Role != "admin" {
		jsonError(w, 403, "Unauthorized")
		return
	}

	path := r.URL.Path
	switch path {
	case "/api/system/reboot-service":
		jsonOk(w, map[string]interface{}{"ok": true, "message": "NimOS restarting..."})
		go func() {
			time.Sleep(1 * time.Second)
			runSafe("sudo", "systemctl", "restart", "nimos")
		}()
	case "/api/system/reboot":
		jsonOk(w, map[string]interface{}{"ok": true, "message": "System rebooting..."})
		go func() {
			time.Sleep(1 * time.Second)
			runSafe("sudo", "reboot")
		}()
	case "/api/system/shutdown":
		jsonOk(w, map[string]interface{}{"ok": true, "message": "System shutting down..."})
		go func() {
			time.Sleep(1 * time.Second)
			runSafe("sudo", "shutdown", "-h", "now")
		}()
	case "/api/system/update/apply":
		handleUpdateApply(w)
	case "/api/terminal":
		handleTerminal(w, r, session)
	default:
		jsonError(w, 404, "Not found")
	}
}

func handleUpdateCheck(w http.ResponseWriter) {
	currentVersion := "0.0.0"
	if data, err := os.ReadFile("/opt/nimos/package.json"); err == nil {
		var pkg map[string]interface{}
		if json.Unmarshal(data, &pkg) == nil {
			if v, ok := pkg["version"].(string); ok {
				currentVersion = v
			}
		}
	}
	latestVersion := "0.0.0"
	if out, ok := runSafe("curl", "-fsSL", "https://raw.githubusercontent.com/andresgv-beep/NimOs-beta-7/main/package.json"); ok {
		var pkg map[string]interface{}
		if json.Unmarshal([]byte(out), &pkg) == nil {
			if v, ok := pkg["version"].(string); ok {
				latestVersion = v
			}
		}
	}
	jsonOk(w, map[string]interface{}{
		"currentVersion":  currentVersion,
		"latestVersion":   latestVersion,
		"updateAvailable": latestVersion != currentVersion,
		"installDir":      "/opt/nimos",
	})
}

func handleUpdateApply(w http.ResponseWriter) {
	script := "/opt/nimos/scripts/update.sh"
	os.MkdirAll("/opt/nimos/scripts", 0755)
	os.MkdirAll("/var/log/nimos", 0755)

	// Si no existe el script, descargarlo de GitHub
	if _, err := os.Stat(script); err != nil {
		logMsg("update.sh not found, downloading from GitHub...")
		// SECURITY: Download update script safely (no shell interpolation)
		_, ok := runSafe("curl", "-fsSL",
			"https://raw.githubusercontent.com/andresgv-beep/NimOs-beta-7/main/scripts/update.sh",
			"-o", "/opt/nimos/scripts/update.sh")
		if !ok {
			// curl returns "" on success sometimes
		}
		// Download checksum file for verification
		checksumOut, csOk := runSafe("curl", "-fsSL",
			"https://raw.githubusercontent.com/andresgv-beep/NimOs-beta-7/main/scripts/update.sh.sha256")
		if csOk && checksumOut != "" {
			// Verify checksum: expected format "sha256hash  filename" or just hash
			expectedHash := strings.Fields(checksumOut)[0]
			actualHash, hashOk := runSafe("sha256sum", script)
			if hashOk {
				actualHashStr := strings.Fields(actualHash)[0]
				if actualHashStr != expectedHash {
					os.Remove(script)
					logMsg("SECURITY: update.sh checksum mismatch! Expected %s, got %s", expectedHash, actualHashStr)
					jsonError(w, 500, "Update script checksum verification failed")
					return
				}
				logMsg("update.sh checksum verified OK")
			}
		} else {
			logMsg("WARNING: No checksum file available for update.sh — proceeding without verification")
		}
		if err2 := os.Chmod(script, 0755); err2 != nil {
			jsonError(w, 500, "Failed to download update script")
			return
		}
	}

	// Verificar que ahora existe
	if _, err := os.Stat(script); err != nil {
		jsonError(w, 400, "Update script not found and could not be downloaded")
		return
	}

	os.Remove("/var/log/nimos/update-result.json")

	cmd := exec.Command("setsid", "bash", script)
	cmd.Dir = "/opt/nimos"
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	logFile, err := os.OpenFile("/var/log/nimos/update.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		jsonError(w, 500, fmt.Sprintf("Failed to start update: %v", err))
		return
	}
	jsonOk(w, map[string]interface{}{"ok": true, "message": "Update started."})
}

func handleUpdateStatus(w http.ResponseWriter) {
	rf := "/var/log/nimos/update-result.json"
	if data, err := os.ReadFile(rf); err == nil {
		var result map[string]interface{}
		if json.Unmarshal(data, &result) == nil {
			result["done"] = true
			jsonOk(w, result)
			return
		}
	}
	jsonOk(w, map[string]interface{}{"done": false})
}

// WARNING: Admin-level RCE endpoint — intentional privileged shell access.
// Protected by: admin session check, audit logging, cwd sanitization, cmd guards.
func handleTerminal(w http.ResponseWriter, r *http.Request, session *DBSession) {
	// SECURITY: Terminal can be disabled via config
	if !isTerminalEnabled() {
		jsonError(w, 403, "Terminal is disabled in system configuration")
		return
	}

	body, _ := readBody(r)
	cmd := bodyStr(body, "cmd")
	cwd := bodyStr(body, "cwd")
	if cmd == "" || len(cmd) > 4096 {
		jsonError(w, 400, "Invalid cmd (max 4096 chars)")
		return
	}

	// SECURITY: Block obviously destructive commands
	dangerousPatterns := []string{
		"rm -rf /\n", "rm -rf / ", "rm -rf /\"", "rm -rf /'",
		":(){ :|:& };:",  // fork bomb
		"mkfs.", "dd if=", "wipefs",
		"> /dev/sd",
	}
	cmdLower := strings.ToLower(cmd)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(cmdLower, strings.ToLower(pattern)) {
			logMsg("TERMINAL BLOCKED [user=%s ip=%s]: %s", session.Username, r.RemoteAddr, cmd)
			jsonError(w, 403, "Command blocked by security policy")
			return
		}
	}

	if cwd == "" {
		cwd = "/root"
	}

	// SECURITY: Validate cwd is a real absolute path (no injection via quotes)
	cleanCwd := filepath.Clean(cwd)
	if !filepath.IsAbs(cleanCwd) {
		cleanCwd = "/root"
	}

	// SECURITY: Audit log EVERY terminal command with session info
	username := session.Username
	ip := r.RemoteAddr
	logMsg("TERMINAL [user=%s ip=%s cwd=%s]: %s", username, ip, cleanCwd, cmd)

	// Execute: use sh -c for the command but set WorkDir safely
	c := exec.Command("sh", "-c", cmd)
	c.Dir = cleanCwd
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
	}
	jsonOk(w, map[string]interface{}{"stdout": strings.TrimSpace(string(out)), "stderr": "", "code": code, "cwd": cleanCwd})
}

// isTerminalEnabled checks if terminal access is enabled in config.
// Defaults to true if not set (backward compatible).
func isTerminalEnabled() bool {
	data, err := os.ReadFile("/var/lib/nimos/config/security.json")
	if err != nil {
		return true // default: enabled
	}
	var conf map[string]interface{}
	if json.Unmarshal(data, &conf) != nil {
		return true
	}
	if enabled, ok := conf["terminalEnabled"].(bool); ok {
		return enabled
	}
	return true
}

func handleSystemInfo(w http.ResponseWriter) {
	interfaces := getNetwork()
	hostname, _ := os.Hostname()
	gateway, _ := runShellStatic("ip route | grep default | awk '{print $3}' | head -1")
	if gateway == "" {
		gateway = "—"
	}
	dnsOut, _ := runShellStatic("cat /etc/resolv.conf 2>/dev/null | grep nameserver | awk '{print $2}'")
	var dnsServers []string
	for _, s := range strings.Split(dnsOut, "\n") {
		if s != "" {
			dnsServers = append(dnsServers, s)
		}
	}
	if dnsServers == nil {
		dnsServers = []string{}
	}

	// Find primary interface name
	primaryName := "eth0"
	for _, n := range interfaces {
		if ip, _ := n["ip"].(string); ip != "—" {
			primaryName, _ = n["name"].(string)
			break
		}
	}
	subnetOut, _ := runSafe("ip", "-4", "-o", "addr", "show", primaryName)
	subnet := ""
	for _, line := range strings.Split(subnetOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			subnet = fields[3]
			break
		}
	}
	if subnet == "" {
		subnet = "—"
	}

	jsonOk(w, map[string]interface{}{
		"network": map[string]interface{}{
			"hostname":   hostname,
			"gateway":    gateway,
			"subnet":     subnet,
			"dns":        dnsServers,
			"interfaces": interfaces,
		},
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// SMART — Disk health data via smartctl
// GET /api/disks/smart?disk=sda
// ═══════════════════════════════════════════════════════════════════════════════

func getDiskSmart(diskName string) map[string]interface{} {
	// Sanitize — only allow alphanumeric
	safe := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(diskName, "")
	if safe == "" {
		return map[string]interface{}{"error": "Invalid disk name"}
	}

	result := map[string]interface{}{
		"disk":       safe,
		"healthy":    true,
		"status":     "ok",     // ok | warning | critical
		"temperature": nil,
		"powerOnHours": nil,
		"powerCycles": nil,
		"reallocated": 0,
		"pending":     0,
		"uncorrectable": 0,
		"attributes":  []map[string]interface{}{},
		"smartSupported": false,
		"model":      "",
		"serial":     "",
		"firmware":   "",
	}

	if !hasSmartctl {
		result["error"] = "smartctl not installed"
		return result
	}

	// Get SMART info
	out, ok := runSafe("smartctl", "-i", "-A", "-H", "/dev/"+safe)
	if !ok || out == "" {
		result["error"] = "Could not read SMART data"
		return result
	}

	result["smartSupported"] = true

	// Parse health status
	if strings.Contains(out, "PASSED") {
		result["status"] = "ok"
		result["healthy"] = true
	} else if strings.Contains(out, "FAILED") {
		result["status"] = "critical"
		result["healthy"] = false
	}

	// Parse info section
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Model Family:") || strings.HasPrefix(line, "Device Model:") {
			result["model"] = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
		if strings.HasPrefix(line, "Serial Number:") {
			result["serial"] = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
		if strings.HasPrefix(line, "Firmware Version:") {
			result["firmware"] = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
	}

	// Parse SMART attributes table
	// Format: "ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE"
	var attrs []map[string]interface{}
	inTable := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ID#") && strings.Contains(line, "ATTRIBUTE_NAME") {
			inTable = true
			continue
		}
		if inTable && strings.TrimSpace(line) == "" {
			break
		}
		if !inTable {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		id := fields[0]
		name := fields[1]
		value := parseIntDefault(fields[3], 0)
		worst := parseIntDefault(fields[4], 0)
		thresh := parseIntDefault(fields[5], 0)
		rawVal := fields[9]
		// Raw values can be: "0", "32 (Min/Max 20/45)", "17551h+03m+04.440s"
		// Extract the leading number
		rawNum := parseRawSmartValue(rawVal)

		// Per-attribute status for the UI table
		// Only mark warning/critical based on REAL problems, not cosmetic thresholds
		attrStatus := "ok"
		if thresh > 0 && value <= thresh && rawNum > 0 {
			attrStatus = "critical"
		} else if thresh > 0 && value <= thresh && rawNum == 0 {
			// Value crossed threshold but raw is 0 — cosmetic, not real failure
			attrStatus = "warning"
		}

		attr := map[string]interface{}{
			"id":     id,
			"name":   name,
			"value":  value,
			"worst":  worst,
			"thresh": thresh,
			"raw":    rawNum,
			"rawStr": rawVal,
			"status": attrStatus,
		}
		attrs = append(attrs, attr)

		// ── Disk-level status escalation ──
		// Philosophy: only alert when the user needs to ACT.
		// Synology/TrueNAS approach: real sector problems and temperature, not
		// historical counters or attributes "near threshold" with raw=0.
		//
		// RED (critical) — act now:
		//   Offline_Uncorrectable > 0, Current_Pending rising, value <= thresh with raw > 0
		// YELLOW (warning) — plan replacement:
		//   Reallocated > 0, Pending > 0, temperature > 50°C sustained
		// NO ALERT:
		//   Reported_Uncorrect (historical counter, only goes up)
		//   Spin_Retry_Count with raw=0 (cosmetic threshold)
		//   End-to-End_Error with raw=0 (cosmetic)
		//   Any attr "near threshold" with raw=0

		switch name {
		case "Temperature_Celsius", "Temperature_Internal", "Airflow_Temperature_Cel":
			result["temperature"] = rawNum
			if rawNum > 55 {
				if result["status"] == "ok" {
					result["status"] = "warning"
				}
			}
		case "Power_On_Hours", "Power_On_Hours_and_Msec":
			result["powerOnHours"] = rawNum
		case "Power_Cycle_Count":
			result["powerCycles"] = rawNum

		// ── These indicate REAL problems — escalate disk status ──
		case "Reallocated_Sector_Ct":
			result["reallocated"] = rawNum
			if rawNum > 0 {
				if result["status"] == "ok" {
					result["status"] = "warning"
				}
			}
		case "Current_Pending_Sector":
			result["pending"] = rawNum
			if rawNum > 0 {
				if result["status"] == "ok" {
					result["status"] = "warning"
				}
			}
		case "Offline_Uncorrectable":
			result["uncorrectable"] = rawNum
			if rawNum > 0 {
				result["status"] = "critical"
				result["healthy"] = false
			}
		case "Reallocated_Event_Count":
			if rawNum > 0 {
				if result["status"] == "ok" {
					result["status"] = "warning"
				}
			}

		// ── These are informational — do NOT escalate disk status ──
		// Reported_Uncorrect: historical ECC counter, only goes up, common on
		// desktop drives used in NAS. Not actionable.
		// Runtime_Bad_Block: low counts are normal wear, not actionable.
		// Spin_Retry_Count: raw=0 means no actual retries.
		// End-to-End_Error: raw=0 means no actual errors.
		// UDMA_CRC_Error_Count: cable issue, not disk failure.
		case "Reported_Uncorrect", "Runtime_Bad_Block", "Spin_Retry_Count",
			"End-to-End_Error", "UDMA_CRC_Error_Count", "Command_Timeout":
			// Tracked but not escalated — informational only
		}
	}

	if attrs == nil {
		attrs = []map[string]interface{}{}
	}
	result["attributes"] = attrs

	return result
}

// parseRawSmartValue extracts the leading integer from SMART raw values
// Handles formats like: "0", "17551h+03m+04.440s", "32 (Min/Max 20/45)", "36"
func parseRawSmartValue(raw string) int {
	if raw == "" {
		return 0
	}
	// Extract leading digits
	numStr := ""
	for _, c := range raw {
		if c >= '0' && c <= '9' {
			numStr += string(c)
		} else {
			break
		}
	}
	if numStr == "" {
		return 0
	}
	n, _ := strconv.Atoi(numStr)
	return n
}

// ═══════════════════════════════════════════════════════════════════════════════
// SMART Monitor — Background disk health monitoring
// Runs every 30 minutes, checks all disks, creates notifications on status change
// ═══════════════════════════════════════════════════════════════════════════════

var smartHistory = map[string]string{} // disk name -> last known status ("ok"/"warning"/"critical")
var smartDetailsCache = map[string]SmartDetails{} // disk name -> cached SMART detail metrics
var smartMu sync.Mutex

func startSmartMonitor() {
	// Wait for system to be ready
	time.Sleep(30 * time.Second)

	if !hasSmartctl {
		logMsg("SMART monitor: smartctl not available, monitor disabled")
		return
	}

	logMsg("SMART monitor started (interval: 30min)")

	// Initial scan
	checkAllDisksSmart()

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		checkAllDisksSmart()
	}
}

func checkAllDisksSmart() {
	// Get all disks from lsblk
	out, ok := runSafe("lsblk", "-d", "-n", "-o", "NAME,TYPE")
	if !ok || out == "" {
		return
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "disk" {
			continue
		}
		diskName := fields[0]

		// Skip loop/ram devices
		if strings.HasPrefix(diskName, "loop") || strings.HasPrefix(diskName, "ram") || strings.HasPrefix(diskName, "zram") {
			continue
		}

		smartResult := getDiskSmart(diskName)
		currentStatus, _ := smartResult["status"].(string)
		if currentStatus == "" {
			continue
		}

		// Cache detail metrics for getSmartDetailsForDisk (used by pool health)
		details := SmartDetails{}
		if v, ok := smartResult["reallocated"].(int); ok {
			details.ReallocatedSectors = v
		}
		if v, ok := smartResult["pending"].(int); ok {
			details.PendingSectors = v
		}
		if v, ok := smartResult["uncorrectable"].(int); ok {
			details.Uncorrectable = v
		}
		if v, ok := smartResult["powerOnHours"].(int); ok {
			details.PowerOnHours = v
		}
		if v, ok := smartResult["temperature"].(int); ok {
			details.Temperature = v
		}

		smartMu.Lock()
		prevStatus, existed := smartHistory[diskName]
		smartHistory[diskName] = currentStatus
		smartDetailsCache[diskName] = details
		smartMu.Unlock()

		// Only notify on status changes (not on first scan unless bad)
		if !existed {
			// First scan — only notify if already bad
			if currentStatus == "warning" {
				model, _ := smartResult["model"].(string)
				addNotification("warning", "system",
					fmt.Sprintf("Disco %s requiere atención", diskName),
					fmt.Sprintf("SMART detecta problemas en %s (%s). Revisa la sección Salud.", diskName, model))
			} else if currentStatus == "critical" {
				model, _ := smartResult["model"].(string)
				addNotification("error", "system",
					fmt.Sprintf("Disco %s en riesgo de fallo", diskName),
					fmt.Sprintf("SMART indica errores críticos en %s (%s). Reemplaza el disco lo antes posible.", diskName, model))
			}
			continue
		}

		// Status changed
		if currentStatus != prevStatus {
			model, _ := smartResult["model"].(string)

			switch {
			case currentStatus == "critical" && prevStatus != "critical":
				addNotification("error", "system",
					fmt.Sprintf("Disco %s en riesgo de fallo", diskName),
					fmt.Sprintf("SMART indica errores críticos en %s (%s). Reemplaza el disco lo antes posible.", diskName, model))
				logMsg("SMART CRITICAL: disk %s status changed from %s to critical", diskName, prevStatus)

			case currentStatus == "warning" && prevStatus == "ok":
				addNotification("warning", "system",
					fmt.Sprintf("Disco %s requiere atención", diskName),
					fmt.Sprintf("SMART detecta nuevos problemas en %s (%s). Revisa la sección Salud.", diskName, model))
				logMsg("SMART WARNING: disk %s status changed from ok to warning", diskName)

			case currentStatus == "ok" && prevStatus != "ok":
				addNotification("success", "system",
					fmt.Sprintf("Disco %s recuperado", diskName),
					fmt.Sprintf("SMART de %s (%s) ha vuelto a estado normal.", diskName, model))
				logMsg("SMART OK: disk %s status recovered from %s", diskName, prevStatus)
			}
		}

		// Temperature alert — check for high temp
		if temp, ok := smartResult["temperature"].(int); ok {
			if temp >= 55 {
				addNotification("warning", "system",
					fmt.Sprintf("Temperatura alta en disco %s", diskName),
					fmt.Sprintf("El disco %s está a %d°C. Verifica la ventilación.", diskName, temp))
				logMsg("SMART TEMP WARNING: disk %s at %d°C", diskName, temp)
			}
		}
	}
}

// getSmartSummary returns a summary of all disks' SMART status
// GET /api/disks/smart/summary
func getSmartSummary() map[string]interface{} {
	smartMu.Lock()
	defer smartMu.Unlock()

	disks := make([]map[string]interface{}, 0)
	worstStatus := "ok"

	for name, status := range smartHistory {
		disks = append(disks, map[string]interface{}{
			"name":   name,
			"status": status,
		})
		if status == "critical" {
			worstStatus = "critical"
		} else if status == "warning" && worstStatus != "critical" {
			worstStatus = "warning"
		}
	}

	return map[string]interface{}{
		"disks":       disks,
		"worstStatus": worstStatus,
		"lastCheck":   time.Now().Format(time.RFC3339),
	}
}
