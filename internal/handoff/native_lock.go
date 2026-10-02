package handoff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (n Native) ownsWriter(ctx context.Context, id string, pid int) (bool, error) {
	if !uuidPattern.MatchString(id) || pid <= 1 {
		return false, fail("invalid_package", "invalid native writer identity")
	}
	dir := filepath.Join(n.Config.CodexHome, "thread-writer-locks")
	coord, err := lockNativeFile(filepath.Join(dir, ".coordination.lock"))
	if err != nil {
		return false, err
	}
	defer coord.Close()
	b, err := run(ctx, "", nil, nil, n.Config.Executables.Lsof, "-nP", "-p", strconv.Itoa(pid), "-Fpn")
	if err != nil {
		return false, err
	}
	expected := filepath.Join(dir, id+".lock")
	locks := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "n") && filepath.Dir(line[1:]) == dir && filepath.Base(line[1:]) != ".coordination.lock" {
			locks[line[1:]] = true
		}
	}
	if len(locks) != 1 || !locks[expected] {
		return false, nil
	}
	if err := noSymlinks(expected); err != nil {
		return false, err
	}
	fd, err := syscall.Open(expected, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer syscall.Close(fd)
	err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

// Codex 0.160.0 coordinates writers per UUID in thread-writer-locks. The
// namespace lock must cover opening/acquiring the thread lock: native cleanup
// removes unlocked files, so opening it outside coordination races inode reuse.
func (n Native) LockWriter(ctx context.Context, s Session) (func(), error) {
	if s.Agent != "codex" {
		return func() {}, nil
	}
	if !uuidPattern.MatchString(s.ID) {
		return nil, fail("invalid_package", "invalid native writer UUID")
	}
	dir := filepath.Join(n.Config.CodexHome, "thread-writer-locks")
	if err := noSymlinks(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	coord, err := lockNativeFile(filepath.Join(dir, ".coordination.lock"))
	if err != nil {
		return nil, fail("busy", "Codex writer coordination is busy; retry: %v", err)
	}
	thread, err := lockNativeFile(filepath.Join(dir, s.ID+".lock"))
	coord.Close()
	if err != nil {
		return nil, fail("busy", "Codex conversation %s already has an active writer or its lock is unavailable: %v", s.ID, err)
	}
	unlock := func() { thread.Close() }
	// Also detect older/non-cooperating writers holding this exact transcript.
	if err := n.noOpenTranscript(ctx, s); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func lockNativeFile(path string) (*os.File, error) {
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fail("conflict", "native lock is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	// Leave the file in place; Codex owns stale-file cleanup under coordination.
	return f, nil
}

func (n Native) noOpenTranscript(ctx context.Context, s Session) error {
	b, err := run(ctx, "", nil, nil, n.Config.Executables.Lsof, "-nP", "-Fpcfan", "-u", strconv.Itoa(os.Getuid()))
	if err != nil {
		return fail("unsupported", "cannot verify native transcript writers: %v", err)
	}
	pid, access := "", ""
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = line[1:]
		case 'f':
			access = ""
		case 'a':
			access = line[1:]
		case 'n':
			path := strings.TrimSuffix(line[1:], " (deleted)")
			name := filepath.Base(path)
			if within(n.Config.CodexHome, path) && strings.Contains(name, "-"+s.ID) && strings.Contains(name, ".jsonl") && access != "r" {
				return fail("busy", "Codex conversation %s has transcript writer PID %s", s.ID, pid)
			}
		}
	}
	return nil
}

func codexWriterCompatibility(ctx context.Context, c Config) error {
	table, err := processTable(ctx)
	if err != nil {
		return err
	}
	checked := map[string]bool{}
	for _, p := range table {
		if filepath.Base(p.Executable) != "codex" || checked[p.Executable] || strings.HasPrefix(p.State, "Z") {
			continue
		}
		exe, err := processExecutable(ctx, c, p)
		if err != nil {
			return err
		}
		if checked[exe] {
			continue
		}
		b, err := run(ctx, "", agentEnv(c), nil, exe, "--version")
		if err != nil {
			return fail("unsupported", "cannot verify running Codex writer PID %d: %v", p.PID, err)
		}
		version := strings.TrimSpace(string(b))
		switch version {
		case "codex-cli 0.160.0", "codex-cli 0.159.3", "codex-cli 0.159.0", "codex-cli 0.159.0-alpha.12", "codex-cli 0.159.0-alpha.12.1":
		default:
			return fail("unsupported", "running Codex PID %d has unverified writer-lock version %q", p.PID, version)
		}
		checked[exe] = true
	}
	return nil
}
