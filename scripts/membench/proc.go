package main

import (
	"os"
	"strconv"
	"strings"
)

// procStatusKiB reads a /proc/<pid>/status field (e.g. "VmRSS", "VmHWM") in KiB.
// Returns 0 if the process is gone or the field is absent.
func procStatusKiB(pid int, field string) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	prefix := field + ":"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			f := strings.Fields(strings.TrimPrefix(line, prefix))
			if len(f) >= 1 {
				v, _ := strconv.ParseUint(f[0], 10, 64)
				return v // KiB
			}
		}
	}
	return 0
}

// procTree returns pid plus every descendant, walking /proc/*/stat PPID links.
// Chromium is multi-process, so its footprint is the whole tree, not one PID.
func procTree(root int) []int {
	children := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []int{root}
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid := procPPID(pid)
		if ppid > 0 {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var out []int
	var walk func(int)
	walk = func(p int) {
		out = append(out, p)
		for _, c := range children[p] {
			walk(c)
		}
	}
	walk(root)
	return out
}

// procPPID reads the parent pid from /proc/<pid>/stat. The comm field may contain
// spaces/parens, so parse after the trailing ')'.
func procPPID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(data)
	rp := strings.LastIndex(s, ")")
	if rp < 0 || rp+2 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[rp+2:]) // state, ppid, ...
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// procPssKiB reads the process's proportional set size (Pss) from
// /proc/<pid>/smaps_rollup, in KiB. PSS charges each shared page to a process in
// proportion to how many processes map it, so summing PSS across a multi-process
// tree (Chromium) does NOT double-count the shared pages that plain RSS would.
// Falls back to VmRSS when smaps_rollup is unavailable.
func procPssKiB(pid int) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/smaps_rollup")
	if err != nil {
		return procStatusKiB(pid, "VmRSS")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Pss:") {
			f := strings.Fields(strings.TrimPrefix(line, "Pss:"))
			if len(f) >= 1 {
				v, _ := strconv.ParseUint(f[0], 10, 64)
				return v
			}
		}
	}
	return procStatusKiB(pid, "VmRSS")
}

// treeRSSKiB sums proportional set size (PSS) across a process and all
// descendants (KiB) — the honest "real memory" figure for a multi-process tree,
// with shared pages counted once in aggregate.
func treeRSSKiB(root int) uint64 {
	var total uint64
	for _, pid := range procTree(root) {
		total += procPssKiB(pid)
	}
	return total
}
