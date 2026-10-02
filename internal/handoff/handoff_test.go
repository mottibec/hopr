package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testID = "11111111-2222-4333-8444-555555555555"

func TestMain(m *testing.M) { os.Setenv("TMPDIR", "/private/tmp"); os.Exit(m.Run()) }
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func write(t *testing.T, p string, b []byte) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0700))
	must(t, os.WriteFile(p, b, 0644))
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func repo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	git(t, d, "init", "-b", "feature")
	write(t, filepath.Join(d, "file.txt"), []byte("initial\n"))
	write(t, filepath.Join(d, "deleted.txt"), []byte("delete me\n"))
	write(t, filepath.Join(d, "binary.bin"), []byte{0, 1, 2, 3})
	write(t, filepath.Join(d, "script"), []byte("echo ok\n"))
	write(t, filepath.Join(d, ".gitignore"), []byte("ignored.txt\nomitted.txt\n"))
	must(t, os.Symlink("file.txt", filepath.Join(d, "link")))
	git(t, d, "add", ".")
	git(t, d, "commit", "-m", "initial")
	write(t, filepath.Join(d, "committed.txt"), []byte("unpushed commit\n"))
	git(t, d, "add", ".")
	git(t, d, "commit", "-m", "unpushed")
	write(t, filepath.Join(d, "file.txt"), []byte("staged\n"))
	git(t, d, "add", "file.txt")
	write(t, filepath.Join(d, "file.txt"), []byte("staged\nunstaged\n"))
	write(t, filepath.Join(d, "binary.bin"), []byte{0, 9, 8, 7, 255})
	git(t, d, "add", "binary.bin")
	write(t, filepath.Join(d, "binary.bin"), []byte{0, 9, 8, 7, 254})
	must(t, os.Remove(filepath.Join(d, "deleted.txt")))
	must(t, os.Chmod(filepath.Join(d, "script"), 0755))
	write(t, filepath.Join(d, "untracked with space.txt"), []byte("untracked\n"))
	write(t, filepath.Join(d, "ignored.txt"), []byte("explicit ignored\n"))
	write(t, filepath.Join(d, "omitted.txt"), []byte("omit\n"))
	return d
}
func nativeFixture(t *testing.T, c Config, agent, cwd string) string {
	t.Helper()
	var b []byte
	var p string
	if agent == "codex" {
		p = filepath.Join(c.CodexHome, "sessions", "2026", "10", "02", "rollout-2026-10-02T12-00-00-"+testID+".jsonl")
		rows := []any{map[string]any{"type": "session_meta", "timestamp": "2026-10-02T12:00:00Z", "payload": map[string]any{"id": testID, "cwd": cwd, "source": "cli", "cli_version": "0.160.0"}}, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "keep the exact conversation"}}}}}
		for _, r := range rows {
			x, _ := json.Marshal(r)
			b = append(b, append(x, '\n')...)
		}
	} else {
		p = filepath.Join(c.ClaudeHome, "projects", encodeClaudePath(cwd), testID+".jsonl")
		r := map[string]any{"type": "user", "sessionId": testID, "uuid": "msg-1", "cwd": cwd, "version": "2.1.287", "message": map[string]any{"role": "user", "content": "keep the exact conversation"}}
		b, _ = json.Marshal(r)
		b = append(b, '\n')
	}
	write(t, p, b)
	return p
}
func configFixture(t *testing.T, id, path string) Config {
	d := t.TempDir()
	c := Config{HostID: id, StateDir: filepath.Join(d, "state"), WorkspaceRoot: filepath.Join(d, "workspaces"), DefaultServer: "default", Servers: map[string]string{"default": filepath.Join(d, "server.sock")}, CodexHome: filepath.Join(d, "codex"), ClaudeHome: filepath.Join(d, "claude"), Projects: map[string]Project{"project": {Path: path, IncludeIgnored: []string{"ignored.txt"}}}, Hosts: map[string]Host{}}
	must(t, c.normalize())
	return c
}

type fakeRuntime struct {
	session                  Session
	stopped                  bool
	pane                     string
	terminal                 string
	token                    string
	ready                    bool
	stops, creates, launches int
	preflightError           error
	guardError               error
	readyError               error
}

func (f *fakeRuntime) Preflight(context.Context, Session, bool) error { return f.preflightError }
func (f *fakeRuntime) Inspect(context.Context, string, string) (Session, error) {
	return f.session, nil
}
func (f *fakeRuntime) Guard(_ context.Context, _ Session, stopped bool) error {
	if f.guardError != nil {
		return f.guardError
	}
	if stopped && !f.stopped {
		return fail("busy", "writer alive")
	}
	return nil
}
func (f *fakeRuntime) Stop(context.Context, Session) error            { f.stopped = true; f.stops++; return nil }
func (f *fakeRuntime) Stopped(context.Context, Session) (bool, error) { return f.stopped, nil }
func (f *fakeRuntime) Retire(context.Context, Session) error          { return nil }
func (f *fakeRuntime) FindWorkspace(context.Context, string) (string, string, string, error) {
	return f.pane, f.terminal, f.token, nil
}
func (f *fakeRuntime) CreateWorkspace(context.Context, string, string) (string, string, string, error) {
	f.creates++
	f.pane = "w2:p1"
	f.terminal = "terminal-2"
	f.token = "server-2"
	return f.pane, f.terminal, f.token, nil
}
func (f *fakeRuntime) Launch(_ context.Context, j Journal) error {
	f.launches++
	f.ready = true
	f.stopped = false
	f.session = Session{Host: j.Destination, Server: "default", ServerToken: j.TargetServerToken, Pane: j.TargetPane, Terminal: j.TargetTerminal, Agent: j.Source.Agent, ID: j.Source.ID, CWD: j.TargetPath, Project: j.Source.Project, PID: 5678, ProcessStart: "start-2"}
	return nil
}
func (f *fakeRuntime) Ready(context.Context, Journal) (bool, error) { return f.ready, f.readyError }

type fakeSSH struct {
	target     *Engine
	dropAction string
	dropped    bool
}

func (s *fakeSSH) Call(ctx context.Context, _ string, r Request) (Reply, error) {
	b, _ := json.Marshal(r)
	var decoded Request
	if e := decodeStrict(b, &decoded); e != nil {
		return Reply{}, e
	}
	reply, e := s.target.Receive(ctx, decoded)
	if e == nil && r.Action == s.dropAction && !s.dropped {
		s.dropped = true
		return Reply{}, fail("uncertain", "simulated SSH disconnect after destination effect")
	}
	b, _ = json.Marshal(reply)
	var copy Reply
	json.Unmarshal(b, &copy)
	return copy, e
}
func pair(t *testing.T, agent string) (*Engine, *Engine, *fakeRuntime, *fakeRuntime) {
	t.Helper()
	root := repo(t)
	a := configFixture(t, "m2", root)
	b := configFixture(t, "m4", filepath.Join(t.TempDir(), "different-home", "project"))
	a.Hosts["m4"] = Host{"m4-tail", "m4"}
	b.Hosts["m2"] = Host{"m2-tail", "m2"}
	src := New(a)
	dst := New(b)
	path := nativeFixture(t, a, agent, root)
	sr := &fakeRuntime{session: Session{Host: "m2", Server: "default", ServerToken: "server-1", Pane: "w1:p1", Terminal: "terminal-1", Agent: agent, ID: testID, CWD: root, Project: "project", NativePath: path, PID: 1234, ProcessStart: "start-1"}}
	dr := &fakeRuntime{}
	src.Runtime = sr
	dst.Runtime = dr
	src.Transport = &fakeSSH{target: dst}
	dst.Transport = &fakeSSH{target: src}
	return src, dst, sr, dr
}
func move(t *testing.T, e *Engine) (*Journal, error) {
	t.Helper()
	return e.Move(context.Background(), "w1:p1", "m4", "default", "")
}

func TestWorkspaceRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := repo(t)
	g := Git{"git"}
	stage := t.TempDir()
	w, e := g.Snapshot(ctx, root, []string{"ignored.txt"}, stage)
	must(t, e)
	target := filepath.Join(t.TempDir(), "restored")
	must(t, g.Restore(ctx, w, target, stage))
	restored, e := g.Snapshot(ctx, target, []string{"ignored.txt"}, "")
	must(t, e)
	if restored.Digest != w.Digest {
		t.Fatal("workspace digest differs")
	}
	if git(t, root, "status", "--porcelain") != git(t, target, "status", "--porcelain") {
		t.Fatal("index/worktree status differs")
	}
	if git(t, target, "log", "--format=%s") != "unpushed\ninitial" {
		t.Fatal("unpushed commits missing")
	}
	if _, e = os.Stat(filepath.Join(target, "omitted.txt")); !os.IsNotExist(e) {
		t.Fatal("unconfigured ignored file transferred")
	}
	if e = g.Restore(ctx, w, target, stage); e == nil {
		t.Fatal("overwrote existing checkout")
	}
}
func TestLinkedWorktree(t *testing.T) {
	root := repo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "linked", linked, "HEAD")
	write(t, filepath.Join(linked, "new.txt"), []byte("linked"))
	g := Git{"git"}
	stage := t.TempDir()
	w, e := g.Snapshot(context.Background(), linked, nil, stage)
	must(t, e)
	target := filepath.Join(t.TempDir(), "checkout")
	must(t, g.Restore(context.Background(), w, target, stage))
	s, e := os.Stat(filepath.Join(target, ".git"))
	must(t, e)
	if !s.IsDir() {
		t.Fatal("copied linked .git pointer")
	}
	if git(t, target, "rev-parse", "HEAD") != git(t, linked, "rev-parse", "HEAD") {
		t.Fatal("HEAD differs")
	}
}

func TestDetachedHEADRoundTrip(t *testing.T) {
	root := repo(t)
	git(t, root, "checkout", "--detach")
	g := Git{"git"}
	stage := t.TempDir()
	w, err := g.Snapshot(context.Background(), root, []string{"ignored.txt"}, stage)
	must(t, err)
	if w.Branch != "" {
		t.Fatal("fixture is not detached")
	}
	target := filepath.Join(t.TempDir(), "checkout")
	must(t, g.Restore(context.Background(), w, target, stage))
	restored, err := g.Snapshot(context.Background(), target, []string{"ignored.txt"}, "")
	must(t, err)
	if restored.Branch != "" || restored.Digest != w.Digest {
		t.Fatal("detached HEAD or workspace changed")
	}
}

func TestProjectPolicyMismatchDoesNotStop(t *testing.T) {
	for _, kind := range []string{"ignored", "memory"} {
		t.Run(kind, func(t *testing.T) {
			a, b, source, _ := pair(t, "claude")
			project := b.Config.Projects["project"]
			if kind == "ignored" {
				project.IncludeIgnored = nil
			} else {
				project.ClaudeMemory = true
			}
			b.Config.Projects["project"] = project
			if _, err := move(t, a); err == nil || !strings.Contains(err.Error(), "policies must match") {
				t.Fatalf("unexpected result: %v", err)
			}
			if source.stops != 0 {
				t.Fatal("source stopped before policy validation")
			}
		})
	}
}
func TestHandoffsAndReturnTrips(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			a, b, sr, dr := pair(t, agent)
			j, e := move(t, a)
			must(t, e)
			if j.State != "complete" || sr.stops != 1 || dr.creates != 1 || dr.launches != 1 {
				t.Fatalf("bad move %#v", j)
			}
			again, e := a.Recover(context.Background(), j.ID)
			must(t, e)
			if again.State != "complete" || dr.launches != 1 {
				t.Fatal("duplicate launch")
			}
			_, e = a.Move(context.Background(), "w1:p1", "m4", "default", j.ID)
			must(t, e)
			paths, e := b.Native.Find(dr.session)
			must(t, e)
			if len(paths) != 1 {
				t.Fatal(paths)
			}
			f, e := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0600)
			must(t, e)
			var row any
			if agent == "codex" {
				row = map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": "continued on M4"}}
			} else {
				row = map[string]any{"type": "assistant", "uuid": "msg-2", "sessionId": testID, "cwd": j.TargetPath, "message": map[string]any{"content": "continued on M4"}}
			}
			must(t, json.NewEncoder(f).Encode(row))
			must(t, f.Close())
			write(t, filepath.Join(j.TargetPath, "return.txt"), []byte("return changes"))
			back, e := b.Move(context.Background(), dr.session.Pane, "m2", "default", "")
			must(t, e)
			if back.State != "complete" {
				t.Fatal(back)
			}
			if _, e = os.Stat(filepath.Join(back.TargetPath, "return.txt")); e != nil {
				t.Fatal(e)
			}
			if sr.launches != 1 || dr.launches != 1 {
				t.Fatal("unexpected launches")
			}
			original, e := os.ReadFile(filepath.Join(a.Config.Projects["project"].Path, "file.txt"))
			must(t, e)
			if string(original) != "staged\nunstaged\n" {
				t.Fatal("source checkout modified")
			}
		})
	}
}
func TestSourceFailureRecovery(t *testing.T) {
	for _, point := range []string{"before_prepare_remote", "after_prepare_remote", "after_stop", "before_snapshot", "after_snapshot", "before_retire", "after_retire", "before_copy", "after_copy", "before_advance_remote", "after_target_launch"} {
		t.Run(point, func(t *testing.T) {
			a, _, sr, dr := pair(t, "codex")
			fired := false
			a.Fault = func(p string) error {
				if p == point && !fired {
					fired = true
					return fail("uncertain", "crash %s", p)
				}
				return nil
			}
			j, e := move(t, a)
			if e == nil || j == nil || !fired {
				t.Fatalf("fault not reached: %v", e)
			}
			a.Fault = nil
			j, e = a.Recover(context.Background(), j.ID)
			must(t, e)
			if j.State != "complete" || sr.stops != 1 || dr.creates != 1 || dr.launches != 1 {
				t.Fatalf("incorrect recovery: %+v stops %d launches %d", j, sr.stops, dr.launches)
			}
		})
	}
}
func TestTargetFailureRecovery(t *testing.T) {
	for _, point := range []string{"after_receive_copy", "before_restore", "after_restore", "before_import", "after_import", "after_create_workspace", "after_launch", "after_target_ready"} {
		t.Run(point, func(t *testing.T) {
			a, b, _, dr := pair(t, "claude")
			fired := false
			b.Fault = func(p string) error {
				if p == point && !fired {
					fired = true
					return fail("uncertain", "crash %s", p)
				}
				return nil
			}
			j, e := move(t, a)
			if e == nil || j == nil || !fired {
				t.Fatalf("fault not reached: %v", e)
			}
			b.Fault = nil
			j, e = a.Recover(context.Background(), j.ID)
			must(t, e)
			if j.State != "complete" || dr.creates != 1 || dr.launches != 1 {
				t.Fatalf("duplicate effects: creates %d launch %d", dr.creates, dr.launches)
			}
		})
	}
}
func TestUncertainIntentNeverReplayed(t *testing.T) {
	for _, point := range []string{"before_stop", "before_create_workspace", "before_launch"} {
		t.Run(point, func(t *testing.T) {
			a, b, sr, dr := pair(t, "codex")
			fired := false
			fault := func(p string) error {
				if p == point && !fired {
					fired = true
					return fail("uncertain", "crash")
				}
				return nil
			}
			if point == "before_stop" {
				a.Fault = fault
			} else {
				b.Fault = fault
			}
			j, e := move(t, a)
			if e == nil || j == nil {
				t.Fatal("expected uncertainty")
			}
			a.Fault = nil
			b.Fault = nil
			_, e = a.Recover(context.Background(), j.ID)
			if e == nil {
				t.Fatal("ambiguous intent silently replayed")
			}
			if point == "before_stop" && sr.stops != 0 {
				t.Fatal("stop retried")
			}
			if point == "before_create_workspace" && dr.creates != 0 {
				t.Fatal("create retried")
			}
			if dr.launches != 0 {
				t.Fatal("launched after unknown outcome")
			}
		})
	}
}
func TestSSHDisconnectAfterEffects(t *testing.T) {
	for _, action := range []string{"prepare", "copy", "advance"} {
		t.Run(action, func(t *testing.T) {
			a, _, _, dr := pair(t, "codex")
			a.Transport.(*fakeSSH).dropAction = action
			j, e := move(t, a)
			if e == nil {
				t.Fatal("expected disconnect")
			}
			j, e = a.Recover(context.Background(), j.ID)
			must(t, e)
			if j.State != "complete" || dr.launches != 1 || dr.creates != 1 {
				t.Fatal("duplicate destination")
			}
		})
	}
}
func TestPreflightRefusalsDoNotStop(t *testing.T) {
	for _, kind := range []string{"identity", "version", "writers", "lfs", "submodule", "conflict", "secrets"} {
		t.Run(kind, func(t *testing.T) {
			a, b, sr, _ := pair(t, "codex")
			switch kind {
			case "identity":
				sr.session.ID = ""
			case "version":
				b.Runtime.(*fakeRuntime).preflightError = fail("unsupported", "version")
			case "writers":
				sr.guardError = fail("busy", "writer")
			case "lfs":
				write(t, filepath.Join(sr.session.CWD, ".lfsconfig"), []byte("lfs"))
			case "submodule":
				write(t, filepath.Join(sr.session.CWD, ".gitmodules"), []byte("module"))
			case "conflict":
				write(t, filepath.Join(sr.session.CWD, ".git", "MERGE_HEAD"), []byte("x"))
			case "secrets":
				f, e := os.OpenFile(sr.session.NativePath, os.O_APPEND|os.O_WRONLY, 0600)
				must(t, e)
				fmt.Fprintln(f, `{"type":"response_item","payload":{"text":"-----BEGIN PRIVATE KEY-----"}}`)
				f.Close()
			}
			_, e := move(t, a)
			if e == nil {
				t.Fatal("expected refusal")
			}
			if sr.stops != 0 {
				t.Fatal("stopped before preflight rejection")
			}
		})
	}
}
func TestDivergencePreservesNativeFile(t *testing.T) {
	a, b, _, _ := pair(t, "codex")
	target := nativeFixture(t, b.Config, "codex", b.Config.Projects["project"].Path)
	f, e := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0600)
	must(t, e)
	fmt.Fprintln(f, `{"type":"response_item","payload":{"text":"divergent"}}`)
	f.Close()
	before, e := os.ReadFile(target)
	must(t, e)
	_, e = move(t, a)
	if e == nil {
		t.Fatal("accepted divergence")
	}
	after, e := os.ReadFile(target)
	must(t, e)
	if !bytes.Equal(before, after) {
		t.Fatal("overwrote divergent transcript")
	}
}
func TestInvalidPackages(t *testing.T) {
	a, _, _, _ := pair(t, "codex")
	j, e := move(t, a)
	must(t, e)
	b, e := os.ReadFile(filepath.Join(a.Store.Dir(j.ID), "package.json"))
	must(t, e)
	if _, e = decodePackage(b, "bad"); e == nil {
		t.Fatal("accepted checksum")
	}
	var p Package
	must(t, json.Unmarshal(b, &p))
	for _, path := range []string{"../escape", "/absolute", ".git/config", "nested/.git/config", "a/../../b"} {
		p.Workspace.Files[0].Path = path
		raw, _ := json.Marshal(p)
		if _, e = decodePackage(raw, digest(raw)); e == nil {
			t.Fatalf("accepted path %s", path)
		}
	}
}
func TestImportCrashReconciliation(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "session.jsonl")
	write(t, p, []byte("old\n"))
	plan := ImportPlan{[]ImportFile{{p, digest([]byte("old\n")), []byte("old\nnew\n")}}}
	must(t, ApplyImport(plan, filepath.Join(d, "backup")))
	must(t, ApplyImport(plan, filepath.Join(d, "backup")))
	write(t, p, []byte("someone else wrote\n"))
	if ApplyImport(plan, filepath.Join(d, "backup")) == nil {
		t.Fatal("clobbered concurrent writer")
	}
}
func TestLockExcludesConcurrentRequests(t *testing.T) {
	s := Store{filepath.Join(t.TempDir(), "state")}
	unlock, e := s.Lock("test")
	must(t, e)
	if _, e = s.Lock("test"); e == nil {
		t.Fatal("lock not exclusive")
	}
	unlock()
	unlock, e = s.Lock("test")
	must(t, e)
	unlock()
}
func TestPathValidation(t *testing.T) {
	f := File{Path: "link", Kind: "symlink", Mode: 0644, Data: []byte("../outside")}
	f.SHA256 = digest(f.Data)
	if validateFiles([]File{f}) == nil {
		t.Fatal("external symlink accepted")
	}
	f.Data = []byte("dir")
	f.SHA256 = digest(f.Data)
	g := File{Path: "link/file", Kind: "file", Mode: 0644, SHA256: digest(nil)}
	if validateFiles([]File{f, g}) == nil {
		t.Fatal("symlink traversal accepted")
	}
	g.Path = "LINK"
	if validateFiles([]File{f, g}) == nil {
		t.Fatal("case collision accepted")
	}
}
func TestCLIUsageJSON(t *testing.T) {
	var out, errout bytes.Buffer
	if Main(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &errout) != 0 || !strings.Contains(out.String(), "hopr move") {
		t.Fatal(out.String())
	}
	out.Reset()
	code := Main(context.Background(), []string{"--json", "--config", "/nonexistent/hopr", "doctor"}, strings.NewReader(""), &out, &errout)
	if code != 2 {
		t.Fatal(code)
	}
	var v Output
	must(t, json.Unmarshal(out.Bytes(), &v))
	if v.OK || v.Error.Code != "configuration" {
		t.Fatal(v)
	}
}
