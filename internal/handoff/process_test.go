package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const historyHelperPath = "computer-use/Codex Computer Use.app/Contents/SharedSupport/SkyComputerUseClient.app/Contents/MacOS/SkyComputerUseClient"

// These subprocesses are copies of this test binary, never real agents.
func processFixtureMain(role string) {
	if role == "native-writer" {
		nativeWriterFixture()
		return
	}
	root := os.Getenv("HOPR_PROCESS_ROOT")
	if role == "grandchild" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if role == "child" {
		os.WriteFile(filepath.Join(root, "child-ready"), []byte("ready"), 0600)
		io.Copy(io.Discard, os.Stdin)
		if _, err := os.Stat(filepath.Join(root, "spawn-grandchild")); err == nil {
			exe, err := os.Executable()
			if err != nil {
				panic(err)
			}
			grandchild := exec.Command(exe)
			grandchild.Env = append(os.Environ(), "HOPR_PROCESS_FIXTURE=grandchild")
			if err := grandchild.Start(); err != nil {
				panic(err)
			}
			os.WriteFile(filepath.Join(root, "grandchild"), []byte(strconv.Itoa(grandchild.Process.Pid)), 0600)
		}
		if os.Getenv("HOPR_PROCESS_LINGER") == "1" {
			for {
				time.Sleep(time.Hour)
			}
		}
		return
	}
	child := exec.Command(os.Getenv("HOPR_PROCESS_CHILD"), "computer-history", "mcp")
	child.Env = append(os.Environ(), "HOPR_PROCESS_FIXTURE=child")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := child.StdinPipe()
	if err != nil {
		panic(err)
	}
	if err = child.Start(); err != nil {
		panic(err)
	}
	for {
		if _, err = os.Stat(filepath.Join(root, "child-ready")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.WriteFile(filepath.Join(root, "ready"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
	io.Copy(io.Discard, os.Stdin)
	input.Close()
	if os.Getenv("HOPR_PROCESS_LINGER") != "1" {
		child.Wait()
	}
}

type processFixture struct {
	config  Config
	session Session
	parent  *exec.Cmd
	input   io.WriteCloser
	child   int
}

func newProcessFixture(t *testing.T, linger, unknown bool) *processFixture {
	t.Helper()
	root := t.TempDir()
	c := configFixture(t, "fixture", root)
	exe, err := os.Executable()
	must(t, err)
	data, err := os.ReadFile(exe)
	must(t, err)
	child := filepath.Join(c.CodexHome, historyHelperPath)
	if unknown {
		child = filepath.Join(root, "unknown-service")
	}
	write(t, child, data)
	must(t, os.Chmod(child, 0700))
	parent := exec.Command(exe)
	parent.Dir = root
	parent.Env = append(os.Environ(), "HOPR_PROCESS_FIXTURE=parent", "HOPR_PROCESS_ROOT="+root, "HOPR_PROCESS_CHILD="+child)
	if linger {
		parent.Env = append(parent.Env, "HOPR_PROCESS_LINGER=1")
	}
	parent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := parent.StdinPipe()
	must(t, err)
	must(t, parent.Start())
	f := &processFixture{config: c, parent: parent, input: input}
	t.Cleanup(func() {
		input.Close()
		if b, err := os.ReadFile(filepath.Join(root, "grandchild")); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil && pid > 1 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if f.child > 1 {
			syscall.Kill(f.child, syscall.SIGKILL)
		}
		parent.Process.Kill()
		parent.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(root, "ready"))
		if err == nil {
			f.child, err = strconv.Atoi(string(b))
			must(t, err)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.child == 0 {
		t.Fatal("fixture did not become ready")
	}
	start, err := processStart(context.Background(), parent.Process.Pid)
	must(t, err)
	f.session = Session{Agent: "codex", ID: testID, CWD: root, PID: parent.Process.Pid, ShellPID: os.Getpid(), ProcessStart: start}
	// Only the writer enumeration is controlled here; child discovery uses real ps.
	c.Executables.Lsof = filepath.Join(root, "lsof")
	write(t, c.Executables.Lsof, []byte("#!/bin/sh\nexit 0\n"))
	must(t, os.Chmod(c.Executables.Lsof, 0700))
	f.config = c
	return f
}

func TestKnownCodexHelperDoesNotBlockIdleAgent(t *testing.T) {
	f := newProcessFixture(t, false, false)
	must(t, captureHelpers(context.Background(), f.config, &f.session))
	if len(f.session.Helpers) != 1 || f.session.Helpers[0].PID != f.child {
		t.Fatalf("helper not captured: %+v", f.session.Helpers)
	}
	if err := processGuard(context.Background(), f.config, f.session, false); err != nil {
		t.Fatalf("idle Codex helper should be supported: %v", err)
	}
	output := fmt.Sprintf("p%d\nfcwd\nn%s\n", f.child, f.session.CWD)
	write(t, f.config.Executables.Lsof, []byte("#!/bin/sh\ncat <<'FIXTURE'\n"+output+"FIXTURE\n"))
	must(t, os.Chmod(f.config.Executables.Lsof, 0700))
	must(t, processGuard(context.Background(), f.config, f.session, false))
}

func TestUnknownChildStillBlocksBeforeStop(t *testing.T) {
	f := newProcessFixture(t, false, true)
	err := processGuard(context.Background(), f.config, f.session, false)
	if err == nil || !strings.Contains(err.Error(), "child") {
		t.Fatalf("accepted unknown child: %v", err)
	}
	if err := syscall.Kill(f.session.PID, 0); err != nil {
		t.Fatal("guard stopped source")
	}
}

func TestHelperIdentityAndWriterGuards(t *testing.T) {
	for _, bad := range []string{"start", "executable", "command", "group", "unrecorded", "writer"} {
		t.Run(bad, func(t *testing.T) {
			f := newProcessFixture(t, false, false)
			must(t, captureHelpers(context.Background(), f.config, &f.session))
			switch bad {
			case "start":
				f.session.Helpers[0].Start = "previous PID incarnation"
			case "executable":
				f.session.Helpers[0].Executable = "/tmp/impostor"
			case "command":
				f.session.Helpers[0].Command += " extra"
			case "group":
				f.session.Helpers[0].Group++
			case "unrecorded":
				f.session.Helpers = nil
			case "writer":
				output := fmt.Sprintf("p%d\nfcwd\nn%s\nf5\naw\nn%s\n", f.child, f.session.CWD, filepath.Join(f.session.CWD, "changing.txt"))
				write(t, f.config.Executables.Lsof, []byte("#!/bin/sh\ncat <<'FIXTURE'\n"+output+"FIXTURE\n"))
				must(t, os.Chmod(f.config.Executables.Lsof, 0700))
			}
			if err := processGuard(context.Background(), f.config, f.session, false); err == nil {
				t.Fatal("guard accepted unsafe helper")
			}
		})
	}
}

func TestHelperShutdownSurvivesJournalReload(t *testing.T) {
	for _, linger := range []bool{false, true} {
		t.Run(fmt.Sprint(linger), func(t *testing.T) {
			f := newProcessFixture(t, linger, false)
			must(t, captureHelpers(context.Background(), f.config, &f.session))
			store := Store{f.config.StateDir}
			j := &Journal{ID: UUID(), Protocol: Protocol, Source: f.session, Role: "source", State: "prepared", Intent: "stop"}
			must(t, store.Save(j))
			must(t, f.input.Close())
			must(t, f.parent.Wait())
			loaded, err := store.Load(j.ID)
			must(t, err)
			if !loaded.Source.equal(f.session) {
				t.Fatal("helper identity was lost in journal")
			}
			done, err := (&Herdr{}).Stopped(context.Background(), loaded.Source)
			must(t, err)
			if done == linger {
				t.Fatalf("stopped=%v with surviving helper=%v", done, linger)
			}
			if linger {
				changed := loaded.Source
				changed.Helpers = append([]ProcessIdentity(nil), changed.Helpers...)
				changed.Helpers[0].Start = "previous PID incarnation"
				if done, err := sourceStopped(context.Background(), changed); err == nil || done {
					t.Fatal("accepted reused helper PID")
				}
				if err := processGuard(context.Background(), f.config, loaded.Source, true); err == nil {
					t.Fatal("export guard accepted orphan helper")
				}
				// The fixture helper is deliberately terminated by this test, never by Hopr.
				must(t, syscall.Kill(f.child, syscall.SIGTERM))
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					done, err = (&Herdr{}).Stopped(context.Background(), loaded.Source)
					must(t, err)
					if done {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !done {
					t.Fatal("stopped helper still blocks recovery")
				}
			}
			must(t, processGuard(context.Background(), f.config, loaded.Source, true))
		})
	}
}

func TestHelperProcessGroupStillBlocksAfterHelperExits(t *testing.T) {
	f := newProcessFixture(t, false, false)
	must(t, captureHelpers(context.Background(), f.config, &f.session))
	write(t, filepath.Join(f.session.CWD, "spawn-grandchild"), []byte("yes"))
	must(t, f.input.Close())
	must(t, f.parent.Wait())
	done, err := sourceStopped(context.Background(), f.session)
	must(t, err)
	if done {
		t.Fatal("accepted live helper process group after original helper exited")
	}
	if err := processGuard(context.Background(), f.config, f.session, true); err == nil {
		t.Fatal("export allowed with surviving helper group")
	}
}

type processRuntime struct {
	*fakeRuntime
	f *processFixture
}

func (r *processRuntime) Guard(ctx context.Context, s Session, stopped bool) error {
	return processGuard(ctx, r.f.config, s, stopped)
}
func (r *processRuntime) Stop(_ context.Context, _ Session) error {
	r.stops++
	if err := r.f.input.Close(); err != nil {
		return err
	}
	return r.f.parent.Wait()
}
func (r *processRuntime) Stopped(ctx context.Context, s Session) (bool, error) {
	return sourceStopped(ctx, s)
}

func TestCoordinatorNeverExportsWhileHelperSurvives(t *testing.T) {
	ctx := context.Background()
	src, dst, sr, dr := pair(t, "codex")
	f := newProcessFixture(t, true, false)
	sr.session.PID, sr.session.ShellPID, sr.session.ProcessStart = f.session.PID, f.session.ShellPID, f.session.ProcessStart
	must(t, captureHelpers(ctx, f.config, &sr.session))
	r := &processRuntime{sr, f}
	src.Runtime = r
	j, err := move(t, src)
	if err == nil || j == nil || j.State != "prepared" || j.Intent != "stop" {
		t.Fatalf("unexpected move: %+v, %v", j, err)
	}
	if len(j.Source.Helpers) != 1 {
		t.Fatal("missing durable helper")
	}
	if _, err := os.Stat(filepath.Join(src.Store.Dir(j.ID), "package.json")); !os.IsNotExist(err) {
		t.Fatal("exported while helper survived")
	}
	if dr.launches != 0 {
		t.Fatal("launched destination with surviving source helper")
	}
	// A new coordinator instance must recover using the journal, not memory.
	restarted := New(src.Config)
	restarted.Runtime, restarted.Transport = r, src.Transport
	for range 2 {
		if _, err := restarted.Recover(ctx, j.ID); err == nil {
			t.Fatal("recovery ignored surviving helper")
		}
	}
	if r.stops != 1 || dr.launches != 0 {
		t.Fatal("replayed stop or launch")
	}
	must(t, syscall.Kill(f.child, syscall.SIGTERM))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		done, err := sourceStopped(ctx, sr.session)
		must(t, err)
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	complete, err := restarted.Recover(ctx, j.ID)
	must(t, err)
	if complete.State != "complete" || r.stops != 1 || dr.launches != 1 {
		t.Fatalf("bad recovery: %+v", complete)
	}
	_, err = restarted.Recover(ctx, j.ID)
	must(t, err)
	if dr.launches != 1 {
		t.Fatal("duplicate launch")
	}
	target, err := dst.Store.Load(j.ID)
	must(t, err)
	if !target.Source.equal(j.Source) {
		t.Fatal("target lost helper metadata")
	}
}

// Kept separate from process tests so the real selected pane is read only.
func TestSelectedSessionHelperPreflight(t *testing.T) {
	pane := os.Getenv("HOPR_PROBE_PANE")
	if pane == "" {
		t.Skip("set HOPR_PROBE_PANE for an explicitly selected read-only probe")
	}
	c, err := LoadConfig(DefaultConfigPath())
	must(t, err)
	h := &Herdr{Config: c}
	s, err := h.Inspect(context.Background(), pane, c.DefaultServer)
	must(t, err)
	b, err := json.Marshal(s)
	must(t, err)
	t.Log(string(b))
	must(t, h.Guard(context.Background(), s, false))
}
