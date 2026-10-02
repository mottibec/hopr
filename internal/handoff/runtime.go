package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Herdr struct{ Config Config }
type agentSession struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Source string `json:"source"`
}
type paneInfo struct {
	Pane          string        `json:"pane_id"`
	Terminal      string        `json:"terminal_id"`
	Workspace     string        `json:"workspace_id"`
	CWD           string        `json:"cwd"`
	ForegroundCWD string        `json:"foreground_cwd"`
	Agent         string        `json:"agent"`
	State         string        `json:"agent_status"`
	Session       *agentSession `json:"agent_session"`
	Ready         bool          `json:"interactive_ready"`
	Pending       bool          `json:"launch_pending"`
}
type processInfo struct {
	ShellPID  int `json:"shell_pid"`
	Processes []struct {
		PID  int      `json:"pid"`
		Name string   `json:"name"`
		Argv []string `json:"argv"`
		CWD  string   `json:"cwd"`
	} `json:"foreground_processes"`
}
type apiResult struct {
	Type         string      `json:"type"`
	Version      string      `json:"version"`
	Protocol     int         `json:"protocol"`
	Agent        paneInfo    `json:"agent"`
	Pane         paneInfo    `json:"pane"`
	RootPane     paneInfo    `json:"root_pane"`
	Panes        []paneInfo  `json:"panes"`
	ProcessInfo  processInfo `json:"process_info"`
	Integrations []struct {
		Target    string `json:"target"`
		Available bool   `json:"available"`
		State     string `json:"state"`
	} `json:"integrations"`
	Workspaces []struct {
		ID    string `json:"workspace_id"`
		Label string `json:"label"`
	} `json:"workspaces"`
}

func (h *Herdr) call(ctx context.Context, server, method string, params any) (apiResult, error) {
	var result apiResult
	socket := h.Config.Servers[server]
	if socket == "" {
		return result, fail("configuration", "unknown source server %s", server)
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return result, fail("dependency", "Herdr socket %s: %v", socket, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Duration(h.Config.TimeoutSeconds) * time.Second))
	id := UUID()
	if err = json.NewEncoder(conn).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return result, err
	}
	var response struct {
		ID     string    `json:"id"`
		Result apiResult `json:"result"`
		Error  *Error    `json:"error"`
	}
	if err = json.NewDecoder(conn).Decode(&response); err != nil {
		return result, fail("uncertain", "Herdr %s response lost: %v", method, err)
	}
	if response.ID != id {
		return result, fail("uncertain", "Herdr response ID mismatch")
	}
	if response.Error != nil {
		return result, fail("dependency", "Herdr %s: %s", method, response.Error.Message)
	}
	return response.Result, nil
}
func (h *Herdr) token(server string) (string, error) {
	s, e := os.Stat(h.Config.Servers[server])
	if e != nil {
		return "", e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fail("unsupported", "cannot identify server socket")
	}
	return fmt.Sprintf("%s:%d:%d", h.Config.Servers[server], st.Dev, st.Ino), nil
}
func (h *Herdr) Preflight(ctx context.Context, s Session, target bool) error {
	if err := commonPreflight(ctx, h.Config, s, target); err != nil {
		return err
	}
	server := s.Server
	if target {
		server = h.Config.DefaultServer
	}
	r, e := h.call(ctx, server, "ping", map[string]any{})
	if e != nil {
		return e
	}
	if r.Protocol != 22 {
		return fail("unsupported", "Herdr protocol %d is unsupported; verified protocol is 22", r.Protocol)
	}
	// Probe the installed client schema as well as the live server; updates can differ.
	b, e := run(ctx, "", nil, nil, h.Config.Executables.Herdr, "api", "schema", "--json")
	if e != nil {
		return e
	}
	var schema struct {
		Protocol int `json:"protocol"`
	}
	if e = json.Unmarshal(b, &schema); e != nil || schema.Protocol != 22 {
		return fail("unsupported", "Herdr client schema is not protocol 22")
	}
	return h.integrationReady(ctx, server, s.Agent)
}
func (h *Herdr) integrationReady(ctx context.Context, server, agent string) error {
	r, err := h.call(ctx, server, "integration.list", map[string]any{})
	if err != nil {
		return err
	}
	for _, integration := range r.Integrations {
		if integration.Target == agent && integration.Available && integration.State == "current" {
			return nil
		}
	}
	return fail("dependency", "Herdr %s integration must be installed and current on server %s", agent, server)
}
func (h *Herdr) Inspect(ctx context.Context, pane, server string) (Session, error) {
	s := Session{Host: h.Config.HostID, Server: server, Pane: pane}
	r, e := h.call(ctx, server, "agent.get", map[string]any{"target": pane})
	if e != nil {
		return s, e
	}
	a := r.Agent
	if a.Pane != pane || a.Session == nil || a.Session.Kind != "id" || !uuidPattern.MatchString(a.Session.Value) {
		return s, fail("unsupported", "pane has no exact integration-reported native session UUID")
	}
	s.Agent = a.Session.Agent
	s.ID = a.Session.Value
	s.Terminal = a.Terminal
	s.CWD = a.ForegroundCWD
	if s.Agent != "codex" && s.Agent != "claude" {
		return s, fail("unsupported", "only native Codex/Claude adapters are implemented")
	}
	if a.Session.Source != "herdr:"+s.Agent {
		return s, fail("unsupported", "session identity was not reported by the official integration")
	}
	if a.State != "idle" || !a.Ready || a.Pending {
		return s, fail("busy", "agent must be idle and interactively ready")
	}
	if s.CWD == "" {
		return s, fail("unsupported", "foreground cwd is unavailable")
	}
	s.CWD, e = filepath.EvalSymlinks(s.CWD)
	if e != nil {
		return s, e
	}
	s.ServerToken, e = h.token(server)
	if e != nil {
		return s, e
	}
	p, e := h.call(ctx, server, "pane.process_info", map[string]any{"pane_id": pane})
	if e != nil {
		return s, e
	}
	s.ShellPID = p.ProcessInfo.ShellPID
	if len(p.ProcessInfo.Processes) != 1 {
		return s, fail("unsupported", "expected one foreground native agent; found %d processes", len(p.ProcessInfo.Processes))
	}
	proc := p.ProcessInfo.Processes[0]
	s.PID = proc.PID
	if s.PID <= 1 || s.PID == s.ShellPID || len(proc.Argv) == 0 || filepath.Base(proc.Argv[0]) != s.Agent {
		return s, fail("unsupported", "foreground process is not the native %s executable", s.Agent)
	}
	for _, arg := range proc.Argv {
		if arg == "--remote" || arg == "--remote-control" || arg == "--background" || arg == "--teleport" || arg == "--worktree" || arg == "--add-dir" || arg == "--resume-session" {
			return s, fail("unsupported", "remote/background/multiple-workspace agent mode")
		}
	}
	if s.Agent == "codex" {
		standalone := false
		for _, arg := range proc.Argv {
			if arg == "--no-daemon" {
				standalone = true
			}
		}
		if !standalone {
			return s, fail("unsupported", "Codex must run with --no-daemon; shared app-server shutdown cannot be established from the terminal PID")
		}
	}
	s.ProcessStart, e = processStart(ctx, s.PID)
	if e != nil {
		return s, e
	}
	s.AgentVersion, e = agentVersion(ctx, h.Config, s.Agent)
	return s, e
}
func (h *Herdr) Guard(ctx context.Context, s Session, stopped bool) error {
	token, e := h.token(s.Server)
	if e != nil {
		return e
	}
	if token != s.ServerToken {
		return fail("uncertain", "source Herdr server incarnation changed; pane IDs may have been reused")
	}
	if !stopped {
		current, e := h.Inspect(ctx, s.Pane, s.Server)
		if e != nil {
			return e
		}
		if current.ID != s.ID || current.PID != s.PID || current.ProcessStart != s.ProcessStart || current.CWD != s.CWD || current.Terminal != s.Terminal {
			return fail("conflict", "source selection changed")
		}
	} else {
		done, e := h.Stopped(ctx, s)
		if e != nil {
			return e
		}
		if !done {
			return fail("busy", "source process is still running")
		}
	}
	return processGuard(ctx, h.Config, s, stopped)
}
func (h *Herdr) Stop(ctx context.Context, s Session) error {
	if e := h.Guard(ctx, s, false); e != nil {
		return e
	}
	// Both pinned native versions support double Ctrl+C graceful exit. This
	// never sends a prompt; nonempty drafts may require manual intervention.
	_, e := h.call(ctx, s.Server, "pane.send_keys", map[string]any{"pane_id": s.Pane, "keys": []string{"ctrl+c", "ctrl+c"}})
	if e != nil {
		return e
	}
	deadline := time.Now().Add(time.Duration(h.Config.TimeoutSeconds) * time.Second)
	for time.Now().Before(deadline) {
		done, e := h.Stopped(ctx, s)
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
	return fail("uncertain", "graceful exit was sent but process has not stopped; no export or forced kill occurred")
}
func (h *Herdr) Stopped(ctx context.Context, s Session) (bool, error) {
	err := syscall.Kill(s.PID, 0)
	if err == syscall.ESRCH {
		return true, nil
	}
	if err != nil {
		return false, fail("uncertain", "cannot verify source PID: %v", err)
	}
	start, e := processStart(ctx, s.PID)
	if e != nil {
		return false, e
	}
	if start != s.ProcessStart {
		return false, fail("uncertain", "source PID was reused; process outcome is ambiguous")
	}
	return false, nil
}
func (h *Herdr) Retire(ctx context.Context, s Session) error {
	if err := h.Guard(ctx, s, true); err != nil {
		return err
	}
	r, err := h.call(ctx, s.Server, "pane.list", map[string]any{})
	if err != nil {
		return err
	}
	found := false
	for _, p := range r.Panes {
		if p.Pane == s.Pane && p.Terminal == s.Terminal {
			found = true
		}
	}
	if !found {
		return nil
	}
	r, err = h.call(ctx, s.Server, "pane.process_info", map[string]any{"pane_id": s.Pane})
	if err != nil {
		return err
	}
	if len(r.ProcessInfo.Processes) != 1 || r.ProcessInfo.Processes[0].PID != s.ShellPID {
		return fail("conflict", "source pane is no longer an idle shell; refusing retirement")
	}
	// Remove the selected pane's saved resume reference so a later Herdr
	// restart cannot automatically resurrect the source conversation.
	_, err = h.call(ctx, s.Server, "pane.close", map[string]any{"pane_id": s.Pane})
	return err
}
func (h *Herdr) FindWorkspace(ctx context.Context, label string) (string, string, string, error) {
	r, e := h.call(ctx, h.Config.DefaultServer, "workspace.list", map[string]any{})
	if e != nil {
		return "", "", "", e
	}
	wid := ""
	for _, w := range r.Workspaces {
		if w.Label == label {
			if wid != "" {
				return "", "", "", fail("conflict", "duplicate move workspaces")
			}
			wid = w.ID
		}
	}
	if wid == "" {
		return "", "", "", nil
	}
	r, e = h.call(ctx, h.Config.DefaultServer, "pane.list", map[string]any{"workspace_id": wid})
	if e != nil {
		return "", "", "", e
	}
	if len(r.Panes) != 1 {
		return "", "", "", fail("conflict", "move workspace must have exactly one pane")
	}
	token, e := h.token(h.Config.DefaultServer)
	return r.Panes[0].Pane, r.Panes[0].Terminal, token, e
}
func (h *Herdr) CreateWorkspace(ctx context.Context, label, path string) (string, string, string, error) {
	env := map[string]string{"PATH": agentPATH(h.Config)}
	for _, item := range agentEnv(h.Config) {
		key, value, _ := strings.Cut(item, "=")
		env[key] = value
	}
	r, e := h.call(ctx, h.Config.DefaultServer, "workspace.create", map[string]any{"label": label, "cwd": path, "focus": false, "env": env})
	if e != nil {
		return "", "", "", e
	}
	token, e := h.token(h.Config.DefaultServer)
	return r.RootPane.Pane, r.RootPane.Terminal, token, e
}
func resumeArgs(s Session, path string) []string {
	if s.Agent == "codex" {
		return []string{"resume", s.ID, "--cd", path, "--no-daemon"}
	}
	return []string{"--resume", s.ID}
}
func (h *Herdr) Launch(ctx context.Context, j Journal) error {
	token, e := h.token(h.Config.DefaultServer)
	if e != nil {
		return e
	}
	if token != j.TargetServerToken {
		return fail("uncertain", "destination server incarnation changed")
	}
	r, e := h.call(ctx, h.Config.DefaultServer, "agent.start", map[string]any{"name": "hopr-" + j.ID, "kind": j.Source.Agent, "pane_id": j.TargetPane, "args": resumeArgs(j.Source, j.TargetPath), "timeout_ms": max(4000, h.Config.TimeoutSeconds*1000)})
	if e != nil {
		return e
	}
	if r.Agent.Terminal != j.TargetTerminal {
		return fail("uncertain", "destination terminal changed")
	}
	return nil
}
func (h *Herdr) Ready(ctx context.Context, j Journal) (bool, error) {
	token, e := h.token(h.Config.DefaultServer)
	if e != nil {
		return false, e
	}
	if token != j.TargetServerToken {
		return false, fail("uncertain", "destination server incarnation changed")
	}
	r, e := h.call(ctx, h.Config.DefaultServer, "agent.get", map[string]any{"target": j.TargetPane})
	if e != nil {
		return false, e
	}
	a := r.Agent
	return a.Terminal == j.TargetTerminal && a.CWD == j.TargetPath && a.Session != nil && a.Session.Kind == "id" && a.Session.Value == j.Source.ID && a.Session.Agent == j.Source.Agent && a.Session.Source == "herdr:"+j.Source.Agent && a.Ready && !a.Pending && a.State == "idle", nil
}
func agentVersion(ctx context.Context, c Config, agent string) (string, error) {
	exe := c.Executables.Codex
	if agent == "claude" {
		exe = c.Executables.Claude
	}
	b, e := run(ctx, "", agentEnv(c), nil, exe, "--version")
	if e != nil {
		return "", e
	}
	v := strings.TrimSpace(string(b))
	want := "codex-cli 0.160.0"
	if agent == "claude" {
		want = "2.1.287 (Claude Code)"
	}
	if v != want {
		return "", fail("unsupported", "%s version %q is unsupported; verified version is %q", agent, v, want)
	}
	return v, nil
}
func agentEnv(c Config) []string {
	env := []string{"CODEX_HOME=" + c.CodexHome}
	// Setting even the default CLAUDE_CONFIG_DIR relocates .claude.json.
	// Leave it unset for the standard ~/.claude transcript layout.
	if c.ClaudeHome != Expand("~/.claude") {
		env = append(env, "CLAUDE_CONFIG_DIR="+c.ClaudeHome)
	}
	return env
}
func agentPATH(c Config) string {
	dirs := []string{}
	for _, p := range []string{c.Executables.Codex, c.Executables.Claude, c.Executables.Herdr} {
		if filepath.IsAbs(p) {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	dirs = append(dirs, os.Getenv("PATH"))
	return strings.Join(dirs, ":")
}
func commonPreflight(ctx context.Context, c Config, s Session, target bool) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fail("unsupported", "production runtime currently requires macOS ARM64")
	}
	version, e := agentVersion(ctx, c, s.Agent)
	if e != nil {
		return e
	}
	if s.AgentVersion != "" && version != s.AgentVersion {
		return fail("unsupported", "source/destination agent versions differ")
	}
	if _, e = run(ctx, "", nil, nil, c.Executables.Git, "--version"); e != nil {
		return e
	}
	for _, args := range c.Projects[s.Project].Requirements {
		if len(args) == 0 {
			return fail("configuration", "empty project requirement")
		}
		if _, e = run(ctx, "", nil, nil, args[0], args[1:]...); e != nil {
			return fail("dependency", "project requirement failed: %v", e)
		}
	}
	home := c.CodexHome
	if s.Agent == "claude" {
		home = c.ClaudeHome
	}
	if _, e = os.Stat(filepath.Join(home, "keybindings.json")); e == nil {
		return fail("unsupported", "custom agent keybindings prevent verified graceful exit")
	}
	if target {
		if s.Agent == "codex" {
			if _, e = run(ctx, "", agentEnv(c), nil, c.Executables.Codex, "login", "status"); e != nil {
				return fail("authentication", "destination Codex login is not ready")
			}
		} else {
			b, e := run(ctx, "", agentEnv(c), nil, c.Executables.Claude, "auth", "status", "--json")
			if e != nil {
				return e
			}
			var auth struct {
				LoggedIn bool `json:"loggedIn"`
			}
			if json.Unmarshal(b, &auth) != nil || !auth.LoggedIn {
				return fail("authentication", "destination Claude authentication is not ready")
			}
		}
		if e = privateDir(c.WorkspaceRoot); e != nil {
			return e
		}
		var stat syscall.Statfs_t
		if e = syscall.Statfs(c.WorkspaceRoot, &stat); e != nil {
			return e
		}
		if uint64(stat.Bavail)*uint64(stat.Bsize) < 3*MaxPackage {
			return fail("dependency", "destination needs at least %d MiB available", 3*MaxPackage>>20)
		}
		// No native process of this kind may already be running on the target.
		if e = noAgentProcesses(ctx, s.Agent); e != nil {
			return e
		}
	}
	return nil
}
func processStart(ctx context.Context, pid int) (string, error) {
	b, e := run(ctx, "", nil, nil, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	if e != nil {
		return "", e
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fail("uncertain", "process start time unavailable")
	}
	return v, nil
}
func noAgentProcesses(ctx context.Context, agent string) error {
	b, e := run(ctx, "", nil, nil, "/bin/ps", "-axo", "pid=,comm=")
	if e != nil {
		return e
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && filepath.Base(f[len(f)-1]) == agent {
			return fail("busy", "destination already has a %s process (PID %s); stop it before importing native history", agent, f[0])
		}
	}
	return nil
}
func processGuard(ctx context.Context, c Config, s Session, stopped bool) error {
	// Refuse other processes with cwd/open files in this workspace, and native
	// descendants (MCP servers, shells, background jobs). No process is killed.
	b, e := run(ctx, "", nil, nil, "/bin/ps", "-axo", "pid=,ppid=")
	if e != nil {
		return e
	}
	parents := map[int]int{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			p, _ := strconv.Atoi(f[0])
			pp, _ := strconv.Atoi(f[1])
			parents[p] = pp
		}
	}
	allowed := map[int]bool{s.ShellPID: true}
	for p := os.Getpid(); p > 1; p = parents[p] {
		if allowed[p] {
			break
		}
		allowed[p] = true
	}
	if !stopped {
		allowed[s.PID] = true
	}
	for pid, pp := range parents {
		if pp == s.PID && pid != os.Getpid() {
			return fail("busy", "agent has a child process %d; stop background services/subagents first", pid)
		}
	}
	b, e = run(ctx, "", nil, nil, c.Executables.Lsof, "-nP", "-Fpcfan", "-u", strconv.Itoa(os.Getuid()))
	if e != nil {
		return fail("unsupported", "cannot enumerate workspace writers with lsof: %v", e)
	}
	pid := 0
	access := ""
	fd := ""
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, _ = strconv.Atoi(l[1:])
		case 'f':
			fd = l[1:]
			access = ""
		case 'a':
			access = l[1:]
		case 'n':
			p := l[1:]
			if (within(s.CWD, p) || s.NativePath != "" && p == s.NativePath) && !allowed[pid] && (fd == "cwd" || access == "w" || access == "u" || access == "") {
				return fail("busy", "unresolved workspace/transcript writer PID %d", pid)
			}
		}
	}
	return nil
}

// CommandRuntime is a terminal-manager-neutral JSON contract. The executable
// owns terminal-specific detection; hopr still owns journals, files and SSH.
type CommandRuntime struct{ Config Config }
type AdapterRequest struct {
	Protocol int     `json:"protocol"`
	Action   string  `json:"action"`
	Session  Session `json:"session"`
	Journal  Journal `json:"journal"`
	Pane     string  `json:"pane,omitempty"`
	Server   string  `json:"server,omitempty"`
	Label    string  `json:"label,omitempty"`
	Path     string  `json:"path,omitempty"`
	Stopped  bool    `json:"stopped"`
	Target   bool    `json:"target"`
}
type AdapterReply struct {
	Session     Session `json:"session"`
	Pane        string  `json:"pane"`
	Terminal    string  `json:"terminal"`
	ServerToken string  `json:"server_token"`
	Stopped     bool    `json:"stopped"`
	Ready       bool    `json:"ready"`
	Error       *Error  `json:"error,omitempty"`
}

func (a *CommandRuntime) call(ctx context.Context, r AdapterRequest) (AdapterReply, error) {
	var reply AdapterReply
	r.Protocol = Protocol
	b, e := json.Marshal(r)
	if e != nil {
		return reply, e
	}
	cmd := a.Config.AdapterCommand
	out, e := run(ctx, "", nil, b, cmd[0], cmd[1:]...)
	if e != nil {
		return reply, e
	}
	if e = decodeStrict(out, &reply); e != nil {
		return reply, e
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	return reply, nil
}
func (a *CommandRuntime) Preflight(ctx context.Context, s Session, t bool) error {
	if e := commonPreflight(ctx, a.Config, s, t); e != nil {
		return e
	}
	_, e := a.call(ctx, AdapterRequest{Action: "preflight", Session: s, Target: t})
	return e
}
func (a *CommandRuntime) Inspect(ctx context.Context, p, server string) (Session, error) {
	r, e := a.call(ctx, AdapterRequest{Action: "inspect", Pane: p, Server: server})
	if e == nil {
		r.Session.AgentVersion, e = agentVersion(ctx, a.Config, r.Session.Agent)
	}
	return r.Session, e
}
func (a *CommandRuntime) Guard(ctx context.Context, s Session, b bool) error {
	_, e := a.call(ctx, AdapterRequest{Action: "guard", Session: s, Stopped: b})
	if e != nil {
		return e
	}
	return processGuard(ctx, a.Config, s, b)
}
func (a *CommandRuntime) Stop(ctx context.Context, s Session) error {
	_, e := a.call(ctx, AdapterRequest{Action: "stop", Session: s})
	return e
}
func (a *CommandRuntime) Stopped(ctx context.Context, s Session) (bool, error) {
	r, e := a.call(ctx, AdapterRequest{Action: "stopped", Session: s})
	return r.Stopped, e
}
func (a *CommandRuntime) Retire(ctx context.Context, s Session) error {
	_, e := a.call(ctx, AdapterRequest{Action: "retire", Session: s})
	return e
}
func (a *CommandRuntime) FindWorkspace(ctx context.Context, label string) (string, string, string, error) {
	r, e := a.call(ctx, AdapterRequest{Action: "find_workspace", Label: label})
	return r.Pane, r.Terminal, r.ServerToken, e
}
func (a *CommandRuntime) CreateWorkspace(ctx context.Context, label, path string) (string, string, string, error) {
	r, e := a.call(ctx, AdapterRequest{Action: "create_workspace", Label: label, Path: path})
	return r.Pane, r.Terminal, r.ServerToken, e
}
func (a *CommandRuntime) Launch(ctx context.Context, j Journal) error {
	_, e := a.call(ctx, AdapterRequest{Action: "launch", Journal: j})
	return e
}
func (a *CommandRuntime) Ready(ctx context.Context, j Journal) (bool, error) {
	r, e := a.call(ctx, AdapterRequest{Action: "ready", Journal: j})
	return r.Ready, e
}
