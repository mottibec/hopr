package handoff

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// tmux exposes processes, not conversation identity or semantic agent state.
// Operator attestations are short-lived and bound to an exact native process.
type Tmux struct{ Config Config }

type Attestation struct {
	Session Session   `json:"session"`
	Expires time.Time `json:"expires"`
}

var tmuxPane = regexp.MustCompile(`^%[0-9]+$`)

func (t *Tmux) cmd(ctx context.Context, server string, args ...string) ([]byte, error) {
	if !labelPattern.MatchString(server) {
		return nil, fail("configuration", "tmux server must be a socket label")
	}
	return run(ctx, "", nil, nil, t.Config.Executables.Tmux, append([]string{"-L", server}, args...)...)
}

func (t *Tmux) pane(ctx context.Context, server, pane string) (Session, string, error) {
	var s Session
	if !tmuxPane.MatchString(pane) {
		return s, "", fail("usage", "tmux pane must be an exact %%number ID")
	}
	b, e := t.cmd(ctx, server, "display-message", "-p", "-t", pane, "#{pane_id}\t#{pane_pid}\t#{pane_current_path}\t#{pane_current_command}\t#{socket_path}\t#{pid}")
	if e != nil {
		return s, "", e
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(f) != 6 || f[0] != pane {
		return s, "", fail("unsupported", "unexpected tmux pane fields")
	}
	s = Session{Host: t.Config.HostID, Server: server, Pane: pane, Terminal: f[5] + ":" + pane, ServerToken: f[4] + ":" + f[5], CWD: f[2]}
	s.ShellPID, _ = strconv.Atoi(f[1])
	return s, f[3], nil
}

func (t *Tmux) attestationPath(server, pane string) string {
	return filepath.Join(t.Config.StateDir, "attestations", digest([]byte(server+":"+pane))+".json")
}

func (t *Tmux) Attest(ctx context.Context, pane, server, agent, id string) error {
	if agent != "codex" && agent != "claude" || !uuidPattern.MatchString(id) {
		return fail("usage", "attest requires agent and exact native UUID")
	}
	s, _, e := t.pane(ctx, server, pane)
	if e != nil {
		return e
	}
	s.Agent = agent
	s.ID = id
	b, e := run(ctx, "", nil, nil, "/bin/ps", "-axo", "pid=,ppid=,comm=")
	if e != nil {
		return e
	}
	parents := map[int]int{}
	names := map[int]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 {
			p, _ := strconv.Atoi(f[0])
			pp, _ := strconv.Atoi(f[1])
			parents[p] = pp
			names[p] = filepath.Base(f[len(f)-1])
		}
	}
	var found []int
	for pid, name := range names {
		if name != agent {
			continue
		}
		for p := pid; p > 1; p = parents[p] {
			if p == s.ShellPID {
				found = append(found, pid)
				break
			}
		}
	}
	if len(found) != 1 {
		return fail("unsupported", "expected one native agent process in selected tmux pane")
	}
	s.PID = found[0]
	if s.Agent == "codex" {
		b, e = run(ctx, "", nil, nil, "/bin/ps", "-ww", "-p", strconv.Itoa(s.PID), "-o", "command=")
		if e != nil {
			return e
		}
		if !strings.Contains(" "+string(b), " --no-daemon") {
			return fail("unsupported", "Codex must run with --no-daemon")
		}
	}
	s.ProcessStart, e = processStart(ctx, s.PID)
	if e != nil {
		return e
	}
	s.Project, e = t.Config.ProjectFor(s.CWD)
	if e != nil {
		return e
	}
	s.AgentVersion, e = agentVersion(ctx, t.Config, agent)
	if e != nil {
		return e
	}
	native := Native{t.Config, Store{t.Config.StateDir}}
	paths, e := native.Find(s)
	if e != nil {
		return e
	}
	for _, p := range paths {
		if agent == "codex" || filepath.Base(filepath.Dir(p)) == encodeClaudePath(s.CWD) {
			if s.NativePath != "" {
				return fail("conflict", "multiple matching native sessions")
			}
			s.NativePath = p
		}
	}
	if s.NativePath == "" {
		return fail("unsupported", "exact native transcript was not found")
	}
	if _, e = native.Export(s); e != nil {
		return e
	}
	b, e = json.Marshal(Attestation{s, time.Now().Add(5 * time.Minute)})
	if e != nil {
		return e
	}
	return atomicWrite(t.attestationPath(server, pane), b, 0600)
}

func (t *Tmux) Inspect(ctx context.Context, pane, server string) (Session, error) {
	var a Attestation
	b, e := os.ReadFile(t.attestationPath(server, pane))
	if e != nil {
		return a.Session, fail("action_required", "tmux requires hopr attest --pane %s --server %s --agent <codex|claude> --session <exact-UUID> at an empty idle prompt", pane, server)
	}
	if e = decodeStrict(b, &a); e != nil {
		return a.Session, e
	}
	if time.Now().After(a.Expires) {
		return a.Session, fail("action_required", "tmux idle attestation expired; attest again")
	}
	p, _, e := t.pane(ctx, server, pane)
	if e != nil {
		return a.Session, e
	}
	if p.Terminal != a.Session.Terminal || p.ServerToken != a.Session.ServerToken || p.CWD != a.Session.CWD {
		return a.Session, fail("conflict", "tmux pane changed since attestation")
	}
	start, e := processStart(ctx, a.Session.PID)
	if e != nil {
		return a.Session, e
	}
	if start != a.Session.ProcessStart {
		return a.Session, fail("conflict", "native process changed")
	}
	return a.Session, nil
}

func (t *Tmux) Preflight(ctx context.Context, s Session, target bool) error {
	if e := commonPreflight(ctx, t.Config, s, target); e != nil {
		return e
	}
	b, e := run(ctx, "", nil, nil, t.Config.Executables.Tmux, "-V")
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(b)) != "tmux 3.7b" {
		return fail("unsupported", "verified tmux version is 3.7b")
	}
	return nil
}

func (t *Tmux) Guard(ctx context.Context, s Session, stopped bool) error {
	if !stopped {
		current, e := t.Inspect(ctx, s.Pane, s.Server)
		if e != nil {
			return e
		}
		if current.ID != s.ID || current.PID != s.PID {
			return fail("conflict", "tmux source changed")
		}
	} else {
		done, e := t.Stopped(ctx, s)
		if e != nil {
			return e
		}
		if !done {
			return fail("busy", "source process is running")
		}
	}
	return processGuard(ctx, t.Config, s, stopped)
}

func (t *Tmux) Stop(ctx context.Context, s Session) error {
	if e := t.Guard(ctx, s, false); e != nil {
		return e
	}
	if _, e := t.cmd(ctx, s.Server, "send-keys", "-t", s.Pane, "C-c", "C-c"); e != nil {
		return e
	}
	deadline := time.Now().Add(time.Duration(t.Config.TimeoutSeconds) * time.Second)
	for time.Now().Before(deadline) {
		done, e := t.Stopped(ctx, s)
		if e != nil {
			return e
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fail("uncertain", "source has not exited after graceful exit request")
}

func (t *Tmux) Stopped(ctx context.Context, s Session) (bool, error) {
	return (&Herdr{}).Stopped(ctx, s)
}
func (t *Tmux) Retire(ctx context.Context, s Session) error {
	done, err := t.Stopped(ctx, s)
	if err != nil {
		return err
	}
	if !done {
		return fail("busy", "source still running")
	}
	b, err := t.cmd(ctx, s.Server, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return fail("uncertain", "cannot confirm source tmux retirement: %v", err)
	}
	found := false
	for _, p := range strings.Fields(string(b)) {
		if p == s.Pane {
			found = true
		}
	}
	if !found {
		return nil
	}
	current, command, err := t.pane(ctx, s.Server, s.Pane)
	if err != nil {
		return err
	}
	if current.Terminal != s.Terminal || current.ServerToken != s.ServerToken {
		return fail("conflict", "source tmux pane was reused")
	}
	if command != "zsh" && command != "bash" && command != "sh" {
		return fail("conflict", "source tmux pane is no longer an idle shell")
	}
	if err = processGuard(ctx, t.Config, s, true); err != nil {
		return err
	}
	_, err = t.cmd(ctx, s.Server, "kill-pane", "-t", s.Pane)
	return err
}

func (t *Tmux) FindWorkspace(ctx context.Context, label string) (string, string, string, error) {
	b, e := t.cmd(ctx, t.Config.DefaultServer, "list-panes", "-a", "-F", "#{session_name}\t#{pane_id}")
	if e != nil {
		return "", "", "", e
	}
	found := ""
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 2 && f[0] == label {
			if found != "" {
				return "", "", "", fail("conflict", "duplicate tmux move panes")
			}
			found = f[1]
		}
	}
	if found == "" {
		return "", "", "", nil
	}
	s, _, e := t.pane(ctx, t.Config.DefaultServer, found)
	return s.Pane, s.Terminal, s.ServerToken, e
}

func (t *Tmux) CreateWorkspace(ctx context.Context, label, path string) (string, string, string, error) {
	b, e := t.cmd(ctx, t.Config.DefaultServer, "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", label, "-c", path, "/bin/sleep", "86400")
	if e != nil {
		return "", "", "", e
	}
	s, _, e := t.pane(ctx, t.Config.DefaultServer, strings.TrimSpace(string(b)))
	return s.Pane, s.Terminal, s.ServerToken, e
}

func (t *Tmux) Launch(ctx context.Context, j Journal) error {
	s, command, e := t.pane(ctx, t.Config.DefaultServer, j.TargetPane)
	if e != nil {
		return e
	}
	if s.Terminal != j.TargetTerminal || s.ServerToken != j.TargetServerToken || command != "sleep" {
		return fail("conflict", "tmux move placeholder changed; refusing to replace it")
	}
	exe := t.Config.Executables.Codex
	if j.Source.Agent == "claude" {
		exe = t.Config.Executables.Claude
	}
	args := []string{"respawn-pane", "-k", "-t", j.TargetPane, "-c", j.TargetPath}
	for _, env := range agentEnv(t.Config) {
		args = append(args, "-e", env)
	}
	if t.Config.ClaudeHome == Expand("~/.claude") {
		args = append(args, "/usr/bin/env", "-u", "CLAUDE_CONFIG_DIR")
	}
	args = append(args, exe)
	args = append(args, resumeArgs(j.Source, j.TargetPath)...)
	_, e = t.cmd(ctx, t.Config.DefaultServer, args...)
	return e
}

func (t *Tmux) Ready(ctx context.Context, j Journal) (bool, error) {
	s, e := t.Inspect(ctx, j.TargetPane, t.Config.DefaultServer)
	if e != nil {
		return false, fail("action_required", "attach tmux session hopr-%s on destination; verify idle conversation, then hopr attest --pane %s --agent %s --session %s and hopr recover %s", j.ID, j.TargetPane, j.Source.Agent, j.Source.ID, j.ID)
	}
	return s.ID == j.Source.ID && s.Agent == j.Source.Agent && s.CWD == j.TargetPath && s.Terminal == j.TargetTerminal && s.ServerToken == j.TargetServerToken, nil
}
