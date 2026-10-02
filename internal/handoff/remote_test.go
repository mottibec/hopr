package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This receiver exists only in the test binary. Native lifecycle is simulated;
// SSH, packages, journals, native transcript import and Git restoration are real.
func TestRemoteFixtureReceiver(t *testing.T) {
	root := os.Getenv("HOPR_FIXTURE_ROOT")
	if root == "" {
		t.Skip("test-only SSH receiver")
	}
	if !regexp.MustCompile(`^/private/tmp/hopr-smoke-[A-Za-z0-9]+/(codex|claude)$`).MatchString(root) {
		os.Exit(2)
	}
	req, err := ReadRequest(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	c := Config{HostID: "m4", StateDir: filepath.Join(root, "state"), WorkspaceRoot: filepath.Join(root, "workspaces"), DefaultServer: "default", Servers: map[string]string{"default": filepath.Join(root, "fake.sock")}, CodexHome: filepath.Join(root, "codex"), ClaudeHome: filepath.Join(root, "claude"), Projects: map[string]Project{"project": {Path: filepath.Join(root, "project"), IncludeIgnored: []string{"ignored.txt"}}}, Hosts: map[string]Host{"m2": {SSH: "unused", ID: "m2"}}}
	if c.normalize() != nil {
		os.Exit(2)
	}
	e := New(c)
	f := &fakeRuntime{}
	runtimePath := filepath.Join(root, "runtime.json")
	var saved struct {
		Pane, Terminal, Token string
		Ready                 bool
		Creates, Launches     int
	}
	if b, err := os.ReadFile(runtimePath); err == nil {
		json.Unmarshal(b, &saved)
		f.pane = saved.Pane
		f.terminal = saved.Terminal
		f.token = saved.Token
		f.ready = saved.Ready
		f.creates = saved.Creates
		f.launches = saved.Launches
	}
	e.Runtime = f
	reply, err := e.Receive(context.Background(), req)
	if err != nil {
		reply.Error = wrap(err)
	}
	saved.Pane = f.pane
	saved.Terminal = f.terminal
	saved.Token = f.token
	saved.Ready = f.ready
	saved.Creates = f.creates
	saved.Launches = f.launches
	b, _ := json.Marshal(saved)
	if atomicWrite(runtimePath, b, 0600) != nil {
		os.Exit(2)
	}
	if f.creates > 1 || f.launches > 1 {
		reply.Error = &Error{"conflict", "duplicate remote effect"}
	}
	json.NewEncoder(os.Stdout).Encode(reply)
	os.Exit(0)
}

type realFixtureSSH struct {
	host, root string
	disconnect bool
}

func (s *realFixtureSSH) Call(ctx context.Context, _ string, r Request) (Reply, error) {
	var reply Reply
	b, _ := json.Marshal(r)
	command := "HOPR_FIXTURE_ROOT=" + s.root + " " + filepath.Dir(s.root) + "/hopr-tests -test.run '^TestRemoteFixtureReceiver$'"
	out, e := run(ctx, "", nil, b, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", s.host, command)
	if e != nil {
		return reply, e
	}
	if e = decodeStrict(out, &reply); e != nil {
		return reply, e
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	if r.Action == "advance" && s.disconnect {
		s.disconnect = false
		return Reply{}, fail("uncertain", "injected lost response after real SSH destination launch")
	}
	return reply, nil
}
func TestRealSSHFixture(t *testing.T) {
	host := os.Getenv("HOPR_SMOKE_HOST")
	if host == "" {
		t.Skip("set HOPR_SMOKE_HOST to an explicitly selected SSH destination")
	}
	if !sshPattern.MatchString(host) {
		t.Fatal("invalid SSH alias")
	}
	ctx := context.Background()
	out, e := run(ctx, "", nil, nil, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "mktemp -d /private/tmp/hopr-smoke-XXXXXXXX")
	must(t, e)
	root := strings.TrimSpace(string(out))
	if !regexp.MustCompile(`^/private/tmp/hopr-smoke-[A-Za-z0-9]+$`).MatchString(root) {
		t.Fatal("unexpected remote directory")
	}
	t.Logf("Remote fixture retained at %s:%s", host, root)
	exe, e := os.Executable()
	must(t, e)
	f, e := os.Open(exe)
	must(t, e)
	defer f.Close()
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "cat > "+root+"/hopr-tests && chmod 700 "+root+"/hopr-tests")
	cmd.Stdin = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Run(); e != nil {
		t.Fatalf("copy test binary: %v: %s", e, &stderr)
	}
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			a, _, _, _ := pair(t, agent)
			a.Transport = &realFixtureSSH{host: host, root: root + "/" + agent, disconnect: true}
			j, e := move(t, a)
			if e == nil {
				t.Fatal("expected simulated disconnect after real destination processing")
			}
			j, e = a.Recover(ctx, j.ID)
			must(t, e)
			if j.State != "complete" {
				t.Fatal(j.State)
			}
			_, e = a.Recover(ctx, j.ID)
			must(t, e)
			// Independently ask remote Git for HEAD/index/status; do not trust only the journal.
			target := root + "/" + agent + "/workspaces/project/" + j.ID
			b, e := run(ctx, "", nil, nil, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "git -C "+target+" rev-parse HEAD && git -C "+target+" write-tree && git -C "+target+" status --porcelain")
			must(t, e)
			want := git(t, a.Config.Projects["project"].Path, "rev-parse", "HEAD") + "\n" + git(t, a.Config.Projects["project"].Path, "write-tree") + "\n" + git(t, a.Config.Projects["project"].Path, "status", "--porcelain")
			if strings.TrimSpace(string(b)) != want {
				t.Fatalf("remote Git differs\ngot %s\nwant %s", b, want)
			}
			t.Logf("Verified %s native fixture, workspace, exact Git HEAD/index/status, and lost-response recovery: %s", agent, j.ID)
		})
	}
}
