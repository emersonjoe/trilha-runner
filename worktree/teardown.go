package worktree

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Ordered teardown (DV-13b): before a worktree is removed, the processes
// whose current directory is inside it — a dev server, a watcher, a test the
// agent left running — are stopped: SIGTERM, a grace period, SIGKILL. Then
// the worktree goes. Every step is logged; none of it can stop the removal.

// TeardownGrace is how long a process has to exit after SIGTERM.
const TeardownGrace = 1500 * time.Millisecond

// ProcessesIn answers the processes (other than this one) whose current
// directory is dir or inside it. On Linux it reads /proc/*/cwd; elsewhere it
// asks `lsof -a -d cwd -F pn`; without lsof it answers none and a note
// saying the scan was skipped.
func ProcessesIn(ctx context.Context, dir string) ([]int, string, error) {
	table, note, err := processTable(ctx)
	if err != nil || note != "" {
		return nil, note, err
	}
	return table.in(dir), "", nil
}

// cwdTable is every readable process's current directory, resolved.
type cwdTable map[int]string

func (t cwdTable) in(dir string) []int {
	root := resolve(dir)
	var pids []int
	for pid, cwd := range t {
		if cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids
}

func processTable(ctx context.Context) (cwdTable, string, error) {
	self := os.Getpid()
	table := cwdTable{}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return nil, "", err
		}
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == self {
				continue
			}
			if cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd")); err == nil {
				table[pid] = resolve(cwd)
			}
		}
		return table, "", nil
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil, "process scan skipped: lsof is not installed", nil
	}
	// lsof exits 1 when some process could not be read; its output still holds
	// every process it could.
	out, _ := exec.CommandContext(ctx, lsof, "-a", "-d", "cwd", "-F", "pn").Output()
	pid := 0
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0 && pid != self:
			table[pid] = resolve(line[1:])
		}
	}
	return table, "", nil
}

func alive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Teardown stops every process whose current directory is inside dir. It
// never fails: what it could not do is logged.
func Teardown(ctx context.Context, dir string, log func(string)) {
	if log == nil {
		log = func(string) {}
	}
	pids, note, err := ProcessesIn(ctx, dir)
	if err != nil {
		log("teardown: process scan failed: " + err.Error())
		return
	}
	if note != "" {
		log("teardown: " + note)
		return
	}
	if len(pids) == 0 {
		log("teardown: no process holds " + dir)
		return
	}
	signalEach(pids, syscall.SIGTERM, "", sendSignal, log)
	deadline := time.Now().Add(TeardownGrace)
	for time.Now().Before(deadline) {
		left := 0
		for _, pid := range pids {
			if alive(pid) {
				left++
			}
		}
		if left == 0 {
			log("teardown: every process exited")
			return
		}
		select {
		case <-ctx.Done():
			deadline = time.Now()
		case <-time.After(50 * time.Millisecond):
		}
	}
	var stubborn []int
	for _, pid := range pids {
		if alive(pid) {
			stubborn = append(stubborn, pid)
		}
	}
	signalEach(stubborn, syscall.SIGKILL, fmt.Sprintf(" (still running after %s)", TeardownGrace), sendSignal, log)
}

// signalEach sends sig to each pid and logs the outcome, with why after a
// success. A process can exit between the scan and the signal — a `sleep`
// a shell started — so an error is logged, never fatal.
func signalEach(pids []int, sig syscall.Signal, why string, send func(int, syscall.Signal) error, log func(string)) {
	name := map[syscall.Signal]string{syscall.SIGTERM: "SIGTERM", syscall.SIGKILL: "SIGKILL"}[sig]
	for _, pid := range pids {
		if err := send(pid, sig); err != nil {
			log(fmt.Sprintf("teardown: %s %d: %v", name, pid, err))
			continue
		}
		log(fmt.Sprintf("teardown: %s %d%s", name, pid, why))
	}
}

func sendSignal(pid int, sig syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(sig)
}

// Clean tears a run's worktree down in order — its processes, then the
// worktree — keeping the branch.
func (m Manager) Clean(ctx context.Context, key string, log func(string)) error {
	if log == nil {
		log = func(string) {}
	}
	Teardown(ctx, m.Path(key), log)
	if err := m.Remove(ctx, key); err != nil {
		log("teardown: remove worktree: " + err.Error())
		return err
	}
	log("teardown: worktree " + m.Path(key) + " removed")
	return nil
}
