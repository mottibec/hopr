package handoff

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestHerdrLaunchUsesValidUniqueAgentName(t *testing.T) {
	names := map[string]bool{}
	h, token := apiFixture(t, func(method string, raw json.RawMessage) any {
		if method == "pane.get" {
			return map[string]any{"pane": paneInfo{Terminal: "target"}}
		}
		if method != "agent.start" {
			t.Errorf("unexpected %s", method)
		}
		var params struct {
			Name string `json:"name"`
		}
		must(t, json.Unmarshal(raw, &params))
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(params.Name) {
			t.Errorf("invalid Herdr agent name: %q", params.Name)
		}
		if names[params.Name] {
			t.Errorf("different moves share agent name %s", params.Name)
		}
		names[params.Name] = true
		return map[string]any{"agent": paneInfo{Terminal: "target"}}
	})
	for _, id := range []string{testID, "11111111-2222-4333-8444-555555555556"} {
		must(t, h.Launch(context.Background(), Journal{ID: id, TargetServerToken: token, TargetTerminal: "target", TargetPane: "w2:p1", Source: Session{Agent: "codex", ID: testID}}))
	}
}

func TestHerdrLaunchRetriesOnlyConfirmedBusyRejection(t *testing.T) {
	for _, mode := range []string{"busy", "input-failed", "changed-pane", "changed-after-busy"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			h, token := apiFixture(t, func(method string, raw json.RawMessage) any {
				if method == "pane.get" {
					terminal := "target"
					if mode == "changed-pane" || mode == "changed-after-busy" && calls == 1 {
						terminal = "replaced"
					}
					return map[string]any{"pane": paneInfo{Terminal: terminal}}
				}
				if method != "agent.start" {
					t.Errorf("unexpected %s", method)
				}
				calls++
				if mode == "input-failed" {
					return &Error{Code: "agent_start_input_failed", Message: "input outcome is not known"}
				}
				if calls == 1 {
					return &Error{Code: "agent_pane_busy", Message: "shell is starting"}
				}
				return map[string]any{"agent": paneInfo{Terminal: "target"}}
			})
			err := h.Launch(context.Background(), Journal{ID: testID, TargetServerToken: token, TargetTerminal: "target", TargetPane: "w2:p1", Source: Session{Agent: "claude", ID: testID}})
			if (err == nil) != (mode == "busy") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			want := 1
			if mode == "busy" {
				want = 2
			} else if mode == "changed-pane" {
				want = 0
			}
			if calls != want {
				t.Fatalf("launch requests=%d; want %d", calls, want)
			}
		})
	}
}

func TestHerdrReadyBeforeFirstPromptUsesNativeWriter(t *testing.T) {
	c := configFixture(t, "m4", t.TempDir())
	pid := startNativeWriter(t, c, testID, false)
	for _, mode := range []string{"valid", "wrong-argv", "wrong-cwd", "wrong-session", "other-writer", "pending"} {
		t.Run(mode, func(t *testing.T) {
			j := Journal{TargetPane: "w2:p1", TargetTerminal: "target", TargetPath: "/target", Source: Session{Agent: "codex", ID: testID}}
			h, token := apiFixture(t, func(method string, raw json.RawMessage) any {
				if method == "agent.get" {
					a := paneInfo{Agent: "codex", Terminal: "target", CWD: "/target", State: "idle", Ready: true, Pending: mode == "pending"}
					if mode == "wrong-session" {
						a.Session = &agentSession{Agent: "codex", Kind: "id", Value: UUID(), Source: "herdr:codex"}
					}
					return map[string]any{"agent": a}
				}
				if method != "pane.process_info" {
					t.Fatalf("unexpected method %s", method)
				}
				args := append([]string{"codex"}, resumeArgs(j.Source, j.TargetPath)...)
				cwd := j.TargetPath
				if mode == "wrong-argv" {
					args[2] = UUID()
				}
				if mode == "wrong-cwd" {
					cwd = "/other"
				}
				return map[string]any{"process_info": map[string]any{"shell_pid": 1, "foreground_processes": []any{map[string]any{"pid": pid, "argv": args, "cwd": cwd}}}}
			})
			h.Config.CodexHome, h.Config.Executables = c.CodexHome, c.Executables
			j.TargetServerToken = token
			if mode == "other-writer" {
				j.Source.ID = UUID()
			}
			ready, err := h.Ready(context.Background(), j)
			must(t, err)
			if ready != (mode == "valid") {
				t.Fatalf("ready=%v for %s", ready, mode)
			}
		})
	}
}

func apiFixture(t *testing.T, result func(string, json.RawMessage) any) (*Herdr, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "herdr.sock")
	listener, e := net.Listen("unix", socket)
	must(t, e)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			func() {
				defer c.Close()
				var req struct {
					ID     string          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.NewDecoder(c).Decode(&req) != nil {
					return
				}
				value := result(req.Method, req.Params)
				if err, ok := value.(*Error); ok {
					json.NewEncoder(c).Encode(map[string]any{"id": req.ID, "error": err})
				} else {
					json.NewEncoder(c).Encode(map[string]any{"id": req.ID, "result": value})
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); wg.Wait() })
	h := &Herdr{Config: Config{Servers: map[string]string{"named": socket}, DefaultServer: "named", TimeoutSeconds: 2}}
	token, e := h.token("named")
	must(t, e)
	return h, token
}
func TestHerdrReadyRequiresExactIdentityAndIdle(t *testing.T) {
	for _, bad := range []string{"", "identity", "terminal", "cwd", "working", "pending", "no-readiness", "untrusted-source"} {
		t.Run(bad, func(t *testing.T) {
			a := paneInfo{Pane: "w1:p1", Terminal: "terminal", CWD: "/project", State: "idle", Ready: true, Session: &agentSession{Agent: "codex", Kind: "id", Value: testID, Source: "herdr:codex"}}
			switch bad {
			case "identity":
				a.Session.Value = UUID()
			case "terminal":
				a.Terminal = "reused"
			case "cwd":
				a.CWD = "/wrong"
			case "working":
				a.State = "working"
			case "pending":
				a.Pending = true
			case "no-readiness":
				a.Ready = false
			case "untrusted-source":
				a.Session.Source = "fake"
			}
			h, token := apiFixture(t, func(method string, p json.RawMessage) any {
				if method != "agent.get" {
					t.Errorf("unexpected %s", method)
				}
				return map[string]any{"type": "agent_info", "agent": a}
			})
			ready, e := h.Ready(context.Background(), Journal{TargetPane: "w1:p1", TargetTerminal: "terminal", TargetPath: "/project", TargetServerToken: token, Source: Session{ID: testID, Agent: "codex"}})
			must(t, e)
			if ready != (bad == "") {
				t.Fatalf("ready=%v for %s", ready, bad)
			}
		})
	}
}
func TestHerdrMissingSessionRefRejected(t *testing.T) {
	h, _ := apiFixture(t, func(string, json.RawMessage) any {
		return map[string]any{"type": "agent_info", "agent": map[string]any{"pane_id": "w1:p1", "agent": "codex", "agent_status": "idle", "interactive_ready": true}}
	})
	if _, e := h.Inspect(context.Background(), "w1:p1", "named"); e == nil {
		t.Fatal("accepted missing identity")
	}
}

func TestHerdrManuallyStartedSource(t *testing.T) {
	for _, state := range []string{"idle", "done", "working", "blocked", "unknown", "pending"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			exe := filepath.Join(root, "codex")
			write(t, exe, []byte("#!/bin/sh\nprintf 'codex-cli 0.160.0\\n'\n"))
			must(t, os.Chmod(exe, 0755))
			h, _ := apiFixture(t, func(method string, _ json.RawMessage) any {
				switch method {
				case "agent.get":
					a := map[string]any{"pane_id": "w1:p1", "terminal_id": "terminal", "agent_status": state, "foreground_cwd": root, "agent_session": agentSession{Agent: "codex", Kind: "id", Value: testID, Source: "herdr:codex"}}
					if state == "pending" {
						a["agent_status"] = "idle"
						a["launch_pending"] = true
					}
					// Real manual launches omit interactive_ready entirely.
					return map[string]any{"type": "agent_info", "agent": a}
				case "pane.process_info":
					return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"shell_pid": 1, "foreground_processes": []any{map[string]any{"pid": os.Getpid(), "argv": []string{"codex", "--no-daemon"}}}}}
				default:
					t.Errorf("unexpected method %s", method)
					return nil
				}
			})
			h.Config.Executables.Codex = exe
			_, err := h.Inspect(context.Background(), "w1:p1", "named")
			if (err == nil) != (state == "idle" || state == "done") {
				t.Fatalf("state %s: %v", state, err)
			}
		})
	}
}

func TestHerdrIntegrationReadiness(t *testing.T) {
	for _, state := range []string{"current", "outdated", "not_installed", "missing", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			h, _ := apiFixture(t, func(method string, _ json.RawMessage) any {
				if method != "integration.list" {
					t.Errorf("unexpected method %s", method)
				}
				agent := "codex"
				if state == "missing" {
					agent = "claude"
				}
				return map[string]any{"type": "integration_list", "integrations": []any{map[string]any{"target": agent, "state": state, "available": state != "unavailable"}}}
			})
			err := h.integrationReady(context.Background(), "named", "codex")
			if (err == nil) != (state == "current") {
				t.Fatalf("state %s: %v", state, err)
			}
		})
	}
}
func TestHerdrCreateUsesStructuredParams(t *testing.T) {
	path := "/workspace/quotes ' $(touch nope)"
	h, _ := apiFixture(t, func(method string, b json.RawMessage) any {
		if method != "workspace.create" {
			t.Errorf("method %s", method)
		}
		var p map[string]any
		json.Unmarshal(b, &p)
		if p["cwd"] != path || p["label"] != "hopr-test" {
			t.Errorf("params %s", b)
		}
		return map[string]any{"type": "workspace_created", "root_pane": map[string]any{"pane_id": "w1:p1", "terminal_id": "terminal"}}
	})
	pane, _, _, e := h.CreateWorkspace(context.Background(), "hopr-test", path)
	must(t, e)
	if pane != "w1:p1" {
		t.Fatal(pane)
	}
}
func TestSSHCommandContainsNoPayload(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ssh")
	capture := filepath.Join(dir, "capture")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + capture + ".args'\ncat > '" + capture + ".json'\nprintf '%s\\n' '{\"host\":\"m4\"}'\n"
	write(t, exe, []byte(script))
	must(t, os.Chmod(exe, 0755))
	ssh := SSH{exe, map[string]Host{"m4": {SSH: "user@m4-tail", ID: "m4"}}}
	req := Request{Protocol: Protocol, Action: "prepare", ID: UUID(), Destination: "m4", Source: Session{CWD: "/tmp/evil'; touch /tmp/hopr-injection; #"}}
	_, e := ssh.Call(context.Background(), "m4", req)
	must(t, e)
	args, e := os.ReadFile(capture + ".args")
	must(t, e)
	if strings.Contains(string(args), "evil") || !strings.HasSuffix(string(args), "exec \"$HOME/.local/bin/hopr\" receive\n") {
		t.Fatal(string(args))
	}
	raw, e := os.ReadFile(capture + ".json")
	must(t, e)
	var r Request
	must(t, json.Unmarshal(raw, &r))
	if r.Source.CWD != req.Source.CWD {
		t.Fatal("stdin changed")
	}
}
func TestCommandAdapterJSONContract(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "adapter")
	capture := filepath.Join(dir, "request")
	write(t, exe, []byte("#!/bin/sh\ncat > '"+capture+"'\nprintf '%s\\n' '{\"ready\":true}'\n"))
	must(t, os.Chmod(exe, 0755))
	a := CommandRuntime{Config{AdapterCommand: []string{exe}}}
	j := Journal{ID: UUID(), TargetPath: "/different/home/a ' b"}
	ready, e := a.Ready(context.Background(), j)
	must(t, e)
	if !ready {
		t.Fatal("not ready")
	}
	b, e := os.ReadFile(capture)
	must(t, e)
	var r AdapterRequest
	must(t, json.Unmarshal(b, &r))
	if r.Action != "ready" || r.Protocol != Protocol || r.Journal.TargetPath != j.TargetPath {
		t.Fatal(r)
	}
}
func TestResumeCommandsNeverSubmitInstructions(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		args := resumeArgs(Session{Agent: agent, ID: testID}, "/project with spaces")
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--last") || strings.Contains(joined, "--continue") || strings.Contains(joined, "--print") {
			t.Fatal(args)
		}
		if !strings.Contains(joined, testID) {
			t.Fatal(args)
		}
	}
}

func TestClaudeDefaultConfigDirectoryIsNotRelocated(t *testing.T) {
	c := Config{CodexHome: "/native/codex", ClaudeHome: Expand("~/.claude")}
	for _, env := range agentEnv(c) {
		if strings.HasPrefix(env, "CLAUDE_CONFIG_DIR=") {
			t.Fatal("relocated standard Claude credentials")
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/unrelated/claude")
	b, err := run(context.Background(), "", agentEnv(c), nil, "/usr/bin/env")
	must(t, err)
	if strings.Contains(string(b), "CLAUDE_CONFIG_DIR=") {
		t.Fatal("inherited unrelated Claude home")
	}
	c.ClaudeHome = "/explicit/claude"
	b, err = run(context.Background(), "", agentEnv(c), nil, "/usr/bin/env")
	must(t, err)
	if !strings.Contains(string(b), "CLAUDE_CONFIG_DIR=/explicit/claude") {
		t.Fatal("custom Claude home ignored")
	}
}
func TestStrictJSONAndConfigRejections(t *testing.T) {
	for _, b := range []string{`{"protocol":1} trailing`, `{"protocol":1} {}`, `{"invented":true}`} {
		var r Request
		if decodeStrict([]byte(b), &r) == nil {
			t.Fatal("accepted", b)
		}
	}
	c := configFixture(t, "m2", "/tmp/example")
	c.Hosts["evil"] = Host{SSH: "host;touch /tmp/no", ID: "m4"}
	if c.normalize() == nil {
		t.Fatal("accepted SSH injection")
	}
	c.Hosts = map[string]Host{}
	c.StateDir = "/tmp/example/state"
	if c.normalize() == nil {
		t.Fatal("accepted overlapping state")
	}
}
func TestNativeCanonicalGrowthAndNumbers(t *testing.T) {
	a := Conversation{Agent: "claude", ID: testID, Paths: []string{"/a"}, Data: []byte(`{"cwd":"/a","type":"user","sessionId":"` + testID + `","n":9007199254740993}` + "\n")}
	b := Conversation{Agent: "claude", ID: testID, Paths: []string{"/b"}, Data: []byte(`{"n":9007199254740993,"sessionId":"` + testID + `","type":"user","cwd":"/b"}` + "\n" + `{"type":"assistant","cwd":"/b","sessionId":"` + testID + `"}` + "\n")}
	must(t, prefixHistory(a, b))
	b.Data = []byte(strings.ReplaceAll(string(b.Data), "9007199254740993", "9007199254740992"))
	if prefixHistory(a, b) == nil {
		t.Fatal("lost numeric precision during comparison")
	}
}
func TestNativeUnsupportedAssetsAndAuxiliaryState(t *testing.T) {
	a, _, sr, _ := pair(t, "claude")
	write(t, filepath.Join(strings.TrimSuffix(sr.session.NativePath, ".jsonl"), "subagents", "agent.jsonl"), []byte("auxiliary"))
	if _, e := a.Native.Export(sr.session); e == nil {
		t.Fatal("ignored auxiliary state")
	}
	c := Conversation{Agent: "codex", ID: testID, Paths: []string{"/project"}, Data: []byte(`{"type":"session_meta","payload":{"id":"` + testID + `","cwd":"/project"}}` + "\n" + `{"type":"response_item","payload":{"type":"local_image","path":"/external.png"}}` + "\n")}
	if validateConversation(c) == nil {
		t.Fatal("ignored external image")
	}
}
func TestDirtyDestinationAfterRestorePreserved(t *testing.T) {
	a, b, _, dr := pair(t, "claude")
	b.Fault = func(p string) error {
		if p == "after_restore" {
			return fail("uncertain", "crash")
		}
		return nil
	}
	j, e := move(t, a)
	if e == nil {
		t.Fatal("expected crash")
	}
	remote, e := b.Store.Load(j.ID)
	must(t, e)
	path := filepath.Join(remote.TargetPath, "file.txt")
	write(t, path, []byte("do not overwrite"))
	b.Fault = nil
	_, e = a.Recover(context.Background(), j.ID)
	if e == nil || dr.launches != 0 {
		t.Fatal("used dirty destination")
	}
	got, e := os.ReadFile(path)
	must(t, e)
	if string(got) != "do not overwrite" {
		t.Fatal("destination overwritten")
	}
}
func TestDuplicateUUIDDifferentRequestRejected(t *testing.T) {
	a, b, _, _ := pair(t, "codex")
	j, e := move(t, a)
	must(t, e)
	r := a.request(j, "prepare")
	r.Source.ID = UUID()
	if _, e = b.Receive(context.Background(), r); e == nil {
		t.Fatal("reused UUID for another session")
	}
}
