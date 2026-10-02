package handoff

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func nativeWriterFixture() {
	home, id := os.Getenv("HOPR_WRITER_HOME"), os.Getenv("HOPR_WRITER_ID")
	dir := filepath.Join(home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}
	coord, err := os.OpenFile(filepath.Join(dir, ".coordination.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		panic(err)
	}
	if err = syscall.Flock(int(coord.Fd()), syscall.LOCK_EX); err != nil {
		panic(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		panic(err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		panic(err)
	}
	coord.Close()
	if os.Getenv("HOPR_WRITER_LEGACY") == "1" {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	defer lock.Close()
	path := filepath.Join(home, "sessions", "rollout-fixture-"+id+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	file.WriteString("unrelated session remains unchanged\n")
	os.WriteFile(os.Getenv("HOPR_WRITER_READY"), []byte("ready"), 0600)
	io.Copy(io.Discard, os.Stdin)
}

func startNativeWriter(t *testing.T, c Config, id string, legacy bool) int {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	must(t, err)
	b, err := os.ReadFile(exe)
	must(t, err)
	path := filepath.Join(root, "codex")
	write(t, path, b)
	must(t, os.Chmod(path, 0700))
	ready := filepath.Join(root, "ready")
	cmd := exec.Command(path)
	cmd.Args[0] = "codex"
	cmd.Env = append(os.Environ(), "HOPR_PROCESS_FIXTURE=native-writer", "HOPR_WRITER_HOME="+c.CodexHome, "HOPR_WRITER_ID="+id, "HOPR_WRITER_READY="+ready)
	if legacy {
		cmd.Env = append(cmd.Env, "HOPR_WRITER_LEGACY=1")
	}
	input, err := cmd.StdinPipe()
	must(t, err)
	must(t, cmd.Start())
	t.Cleanup(func() { input.Close(); cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return cmd.Process.Pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native writer fixture failed to start")
	return 0
}

func TestNativeWriterOwnershipRequiresExactUUIDAndHeldLock(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		c := configFixture(t, "m4", t.TempDir())
		pid := startNativeWriter(t, c, testID, legacy)
		n := Native{Config: c}
		owns, err := n.ownsWriter(context.Background(), testID, pid)
		must(t, err)
		if owns == legacy {
			t.Fatalf("writer ownership %v with legacy=%v", owns, legacy)
		}
		owns, err = n.ownsWriter(context.Background(), UUID(), pid)
		must(t, err)
		if owns {
			t.Fatal("accepted another conversation's writer")
		}
	}
}

func TestDestinationAllowsUnrelatedCodexSession(t *testing.T) {
	c := configFixture(t, "m4", t.TempDir())
	c.Executables.Codex = filepath.Join(t.TempDir(), "codex-cli")
	write(t, c.Executables.Codex, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'codex-cli 0.160.0\\n'; fi\n"))
	must(t, os.Chmod(c.Executables.Codex, 0700))
	startNativeWriter(t, c, UUID(), false)
	if err := commonPreflight(context.Background(), c, Session{Agent: "codex", ID: testID, Project: "project"}, true); err != nil {
		t.Fatalf("unrelated Codex session blocked destination: %v", err)
	}
}

func TestNativeWriterLockRejectsSameConversation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		c := configFixture(t, "m4", t.TempDir())
		startNativeWriter(t, c, testID, legacy)
		if unlock, err := (Native{Config: c}).LockWriter(context.Background(), Session{Agent: "codex", ID: testID}); err == nil {
			unlock()
			t.Fatalf("accepted active native writer, legacy=%v", legacy)
		}
	}
}

func TestNativeWriterLocksAreScopedAndRespectCoordination(t *testing.T) {
	c := configFixture(t, "m4", t.TempDir())
	n := Native{Config: c}
	s := Session{Agent: "codex", ID: testID}
	unlock, err := n.LockWriter(context.Background(), s)
	must(t, err)
	defer unlock()
	if other, err := n.LockWriter(context.Background(), s); err == nil {
		other()
		t.Fatal("duplicate writer lock")
	}
	other, err := n.LockWriter(context.Background(), Session{Agent: "codex", ID: UUID()})
	must(t, err)
	other()
	coord, err := lockNativeFile(filepath.Join(c.CodexHome, "thread-writer-locks", ".coordination.lock"))
	must(t, err)
	defer coord.Close()
	if other, err := n.LockWriter(context.Background(), Session{Agent: "codex", ID: UUID()}); err == nil {
		other()
		t.Fatal("ignored native namespace coordination")
	}
}

func TestImportPreservesUnrelatedRunningCodexAndHoldsSelectedLock(t *testing.T) {
	src, dst, _, dr := pair(t, "codex")
	otherID := UUID()
	startNativeWriter(t, dst.Config, otherID, false)
	checked := false
	dst.Fault = func(point string) error {
		if point != "before_import" {
			return nil
		}
		checked = true
		if unlock, err := dst.Native.LockWriter(context.Background(), Session{Agent: "codex", ID: testID}); err == nil {
			unlock()
			t.Fatal("import does not own native writer lock")
		}
		return nil
	}
	j, err := move(t, src)
	must(t, err)
	if j.State != "complete" || dr.launches != 1 || !checked {
		t.Fatalf("move incomplete: %+v", j)
	}
	b, err := os.ReadFile(filepath.Join(dst.Config.CodexHome, "sessions", "rollout-fixture-"+otherID+".jsonl"))
	must(t, err)
	if string(b) != "unrelated session remains unchanged\n" {
		t.Fatal("unrelated transcript changed")
	}
	if unlock, err := dst.Native.LockWriter(context.Background(), Session{Agent: "codex", ID: otherID}); err == nil {
		unlock()
		t.Fatal("unrelated writer was stopped")
	}
	unlock, err := dst.Native.LockWriter(context.Background(), Session{Agent: "codex", ID: testID})
	must(t, err)
	unlock()
}

func TestImportBlockedByWriterThatStartsAfterPreparation(t *testing.T) {
	src, dst, _, dr := pair(t, "codex")
	dst.Fault = func(point string) error {
		if point == "after_restore" {
			startNativeWriter(t, dst.Config, testID, false)
		}
		return nil
	}
	j, err := move(t, src)
	if err == nil || !strings.Contains(err.Error(), "active writer") || j == nil {
		t.Fatalf("expected scoped conflict: %v", err)
	}
	if dr.launches != 0 {
		t.Fatal("launched despite active same-session writer")
	}
	if _, err := os.Stat(filepath.Join(dst.Store.Dir(j.ID), "import-plan.json")); !os.IsNotExist(err) {
		t.Fatal("prepared import while selected thread was running")
	}
}
