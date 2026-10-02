package handoff

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type processRecord struct {
	PID, Parent, Group int
	State, Executable  string
}

func processTable(ctx context.Context) (map[int]processRecord, error) {
	b, err := run(ctx, "", nil, nil, "/bin/ps", "-axo", "pid=,ppid=,pgid=,stat=,comm=")
	if err != nil {
		return nil, err
	}
	result := map[int]processRecord{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var fields [4]string
		for i := range fields {
			line = strings.TrimSpace(line)
			end := strings.IndexAny(line, " \t")
			if end < 0 {
				return nil, fail("unsupported", "cannot parse process inventory")
			}
			fields[i], line = line[:end], line[end:]
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil || pid <= 0 {
			return nil, fail("unsupported", "invalid process inventory identity")
		}
		result[pid] = processRecord{pid, parent, group, fields[3], strings.TrimSpace(line)}
	}
	return result, nil
}

func processIdentity(ctx context.Context, p processRecord) (ProcessIdentity, error) {
	start, err := processStart(ctx, p.PID)
	if err != nil {
		return ProcessIdentity{}, err
	}
	b, err := run(ctx, "", nil, nil, "/bin/ps", "-ww", "-p", strconv.Itoa(p.PID), "-o", "args=")
	if err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{p.PID, p.Parent, p.Group, start, p.Executable, strings.TrimSpace(string(b))}, nil
}

func processExecutable(ctx context.Context, c Config, p processRecord) (string, error) {
	if filepath.IsAbs(p.Executable) {
		return p.Executable, nil
	}
	// macOS ps can report argv[0] instead of the executable's absolute path.
	b, err := run(ctx, "", nil, nil, c.Executables.Lsof, "-nP", "-a", "-p", strconv.Itoa(p.PID), "-d", "txt", "-Fn")
	if err != nil {
		return "", err
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "n") && filepath.IsAbs(line[1:]) && filepath.Base(line[1:]) == p.Executable {
			paths[line[1:]] = true
		}
	}
	if len(paths) != 1 {
		return "", fail("unsupported", "cannot resolve exact executable for PID %d", p.PID)
	}
	for path := range paths {
		return path, nil
	}
	return "", fail("unsupported", "missing process executable")
}

func knownCodexHelper(c Config, p ProcessIdentity) bool {
	// Match the full installed executable and complete command, not a process name.
	history := filepath.Join(c.CodexHome, "computer-use/Codex Computer Use.app/Contents/SharedSupport/SkyComputerUseClient.app/Contents/MacOS/SkyComputerUseClient")
	node := "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"
	return p.Executable == history && p.Command == history+" computer-history mcp" ||
		p.Executable == node && p.Command == node
}

func descendants(table map[int]processRecord, root int) []int {
	var ids []int
	for pid, p := range table {
		seen := map[int]bool{pid: true}
		for parent := p.Parent; parent > 1 && !seen[parent]; parent = table[parent].Parent {
			if parent == root {
				ids = append(ids, pid)
				break
			}
			seen[parent] = true
		}
	}
	sort.Ints(ids)
	return ids
}

func captureHelpers(ctx context.Context, c Config, s *Session) error {
	table, err := processTable(ctx)
	if err != nil {
		return err
	}
	root, ok := table[s.PID]
	if !ok {
		return fail("conflict", "selected source process disappeared")
	}
	s.ProcessGroup = root.Group
	s.Helpers = nil
	for _, pid := range descendants(table, s.PID) {
		p := table[pid]
		if pid == os.Getpid() || strings.HasPrefix(p.State, "Z") {
			continue
		}
		identity, err := processIdentity(ctx, p)
		if err != nil {
			if syscall.Kill(pid, 0) == syscall.ESRCH {
				continue
			}
			return err
		}
		if s.Agent != "codex" || p.Parent != s.PID || !knownCodexHelper(c, identity) || (p.Group != p.PID && p.Group != root.Group) {
			return fail("busy", "unsupported child process %d (%s); stop background jobs/subagents before moving", pid, p.Executable)
		}
		s.Helpers = append(s.Helpers, identity)
	}
	return nil
}

func validateHelpers(ctx context.Context, c Config, s Session, table map[int]processRecord) (map[int]bool, error) {
	allowed := map[int]bool{}
	if root, ok := table[s.PID]; s.ProcessGroup != 0 && (!ok || root.Group != s.ProcessGroup) {
		return nil, fail("conflict", "source process group changed")
	}
	for _, helper := range s.Helpers {
		if helper.PID <= 1 || helper.Parent != s.PID || helper.Start == "" || s.Agent != "codex" || !knownCodexHelper(c, helper) ||
			(helper.Group != helper.PID && helper.Group != s.ProcessGroup) || allowed[helper.PID] {
			return nil, fail("conflict", "invalid recorded helper identity")
		}
		p, exists := table[helper.PID]
		if !exists {
			continue
		}
		current, err := processIdentity(ctx, p)
		if err != nil {
			return nil, err
		}
		if current != helper {
			return nil, fail("conflict", "helper PID %d changed; no stop was sent", helper.PID)
		}
		allowed[p.PID] = true
	}
	for _, pid := range descendants(table, s.PID) {
		if pid == os.Getpid() || strings.HasPrefix(table[pid].State, "Z") {
			continue
		}
		if !allowed[pid] {
			return nil, fail("busy", "unrecorded child process %d (%s); source was not stopped", pid, table[pid].Executable)
		}
	}
	return allowed, nil
}

func sourceStopped(ctx context.Context, s Session) (bool, error) {
	table, err := processTable(ctx)
	if err != nil {
		return false, err
	}
	if p, exists := table[s.PID]; exists {
		start, err := processStart(ctx, s.PID)
		if err != nil {
			return false, err
		}
		if start != s.ProcessStart {
			return false, fail("uncertain", "source PID was reused; process outcome is ambiguous")
		}
		if !strings.HasPrefix(p.State, "Z") {
			return false, nil
		}
	}
	groups := map[int]bool{}
	if s.ProcessGroup > 1 {
		groups[s.ProcessGroup] = true
	}
	for _, helper := range s.Helpers {
		if helper.PID <= 1 || helper.Group <= 1 || helper.Start == "" {
			return false, fail("uncertain", "invalid recorded helper identity")
		}
		groups[helper.Group] = true
		if p, exists := table[helper.PID]; exists {
			start, err := processStart(ctx, helper.PID)
			if err != nil {
				return false, err
			}
			if start != helper.Start {
				return false, fail("uncertain", "helper PID %d was reused; cannot confirm shutdown", helper.PID)
			}
			if !strings.HasPrefix(p.State, "Z") {
				return false, nil
			}
		}
	}
	for _, p := range table {
		if groups[p.Group] && p.PID != s.ShellPID && !strings.HasPrefix(p.State, "Z") {
			return false, nil
		}
	}
	return true, nil
}
