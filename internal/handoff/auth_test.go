package handoff

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthProbeChild(t *testing.T) {
	path := os.Getenv("HOPR_AUTH_TEST_REQUEST")
	if path == "" {
		return
	}
	os.Exit(Main(context.Background(), []string{"auth-probe", path}, strings.NewReader(""), io.Discard, io.Discard))
}

func TestHerdrAuthenticationUsesLaunchContext(t *testing.T) {
	for _, mode := range []string{"success", "logged-out", "invalid-json", "wrong-nonce", "missing-result", "changed-workspace", "new-pane"} {
		t.Run(mode, func(t *testing.T) {
			c := configFixture(t, "home", t.TempDir())
			c.Backend = "herdr"
			c.Executables.Claude = filepath.Join(t.TempDir(), "claude")
			body := "#!/bin/sh\n[ \"$*\" = 'auth status --json' ] || exit 2\n[ \"$HERDR_WORKSPACE_ID\" = probe ] || exit 1\n"
			if mode == "logged-out" {
				body += "printf '{\"loggedIn\":false}'; exit 1\n"
			} else if mode == "invalid-json" {
				body += "printf 'invalid'\n"
			} else {
				body += "printf '{\"loggedIn\":true,\"email\":\"private@example.test\"}'\n"
			}
			write(t, c.Executables.Claude, []byte(body))
			must(t, os.Chmod(c.Executables.Claude, 0700))
			if authReadyDirect(context.Background(), c, "claude") == nil {
				t.Fatal("fixture should fail outside Herdr")
			}
			var dir, label string
			closed, launches := false, 0
			h, _ := apiFixture(t, func(method string, raw json.RawMessage) any {
				switch method {
				case "workspace.create":
					var p struct{ CWD, Label string }
					must(t, json.Unmarshal(raw, &p))
					dir, label = p.CWD, p.Label
					return map[string]any{"workspace": map[string]string{"workspace_id": "probe"}}
				case "layout.apply":
					launches++
					var p struct {
						Workspace string `json:"workspace_id"`
						Root      struct{ Command []string }
					}
					must(t, json.Unmarshal(raw, &p))
					exe, err := os.Executable()
					must(t, err)
					if p.Workspace != "probe" || len(p.Root.Command) != 3 || p.Root.Command[0] != exe || p.Root.Command[1] != "auth-probe" || p.Root.Command[2] != filepath.Join(dir, "request.json") {
						t.Errorf("invalid launch argv: %+v", p)
						return nil
					}
					if mode == "missing-result" {
						return map[string]string{"type": "ok"}
					}
					cmd := exec.Command(exe, "-test.run=^TestAuthProbeChild$")
					cmd.Env = append(os.Environ(), "HOPR_CONFIG=/does-not-exist", "HOPR_AUTH_TEST_REQUEST="+p.Root.Command[2], "HERDR_WORKSPACE_ID=probe", "HERDR_SOCKET_PATH="+c.Servers[c.DefaultServer])
					if b, err := cmd.CombinedOutput(); err != nil {
						t.Errorf("helper failed: %v: %s", err, b)
					}
					b, err := os.ReadFile(filepath.Join(dir, "result.json"))
					must(t, err)
					if strings.Contains(string(b), "private@example.test") || strings.Contains(string(b), "loggedIn") {
						t.Error("raw authentication output was persisted")
					}
					if mode == "wrong-nonce" {
						must(t, atomicWrite(filepath.Join(dir, "result.json"), []byte(`{"id":"`+UUID()+`"}`), 0600))
					}
					return map[string]string{"type": "ok"}
				case "pane.list":
					panes := []paneInfo{{Pane: "probe:p1", CWD: dir}, {Pane: "probe:p2", CWD: dir}}
					if mode == "new-pane" {
						panes = append(panes, paneInfo{Pane: "probe:p3", CWD: dir})
					}
					return map[string]any{"panes": panes}
				case "pane.process_info":
					return map[string]any{"process_info": map[string]any{"shell_pid": 123, "foreground_processes": []any{map[string]any{"pid": 123}}}}
				case "workspace.list":
					if mode == "changed-workspace" {
						return map[string]any{"workspaces": []any{}}
					}
					return map[string]any{"workspaces": []any{map[string]string{"workspace_id": "probe", "label": label}}}
				case "workspace.close":
					closed = true
					return map[string]string{"type": "ok"}
				default:
					t.Errorf("unexpected method %s", method)
					return nil
				}
			})
			c.Servers, c.DefaultServer, c.TimeoutSeconds = h.Config.Servers, h.Config.DefaultServer, 2
			h.Config = c
			err := authReady(context.Background(), c, "claude")
			if (err == nil) != (mode == "success") {
				t.Fatalf("%s: %v", mode, err)
			}
			clean := mode == "success" || mode == "logged-out" || mode == "invalid-json"
			if launches != 1 || closed != clean {
				t.Fatalf("launches=%d closed=%v for %s", launches, closed, mode)
			}
			if _, err := os.Stat(dir); os.IsNotExist(err) != clean {
				t.Fatalf("cleanup=%v for %s", err, mode)
			}
			if (mode == "logged-out" || mode == "invalid-json" || mode == "missing-result") && wrap(err).Code != "authentication" {
				t.Fatalf("wrong error category: %v", err)
			}
		})
	}
}

func TestAuthProbeRejectsWrongContextAndDuplicate(t *testing.T) {
	c := configFixture(t, "home", t.TempDir())
	c.Backend = "herdr"
	c.Executables.Claude = "/usr/bin/true"
	id := UUID()
	dir := filepath.Join(c.StateDir, "auth-probes", id)
	must(t, privateDir(dir))
	req := authProbeRequest{ID: id, Agent: "claude", Workspace: "probe", Config: c}
	b, err := json.Marshal(req)
	must(t, err)
	path := filepath.Join(dir, "request.json")
	must(t, atomicWrite(path, b, 0600))
	t.Setenv("HERDR_SOCKET_PATH", c.Servers[c.DefaultServer])
	t.Setenv("HERDR_WORKSPACE_ID", "wrong")
	if runAuthProbe(context.Background(), path) == nil {
		t.Fatal("accepted a different launch environment")
	}
	t.Setenv("HERDR_WORKSPACE_ID", "probe")
	must(t, runAuthProbe(context.Background(), path))
	if err := runAuthProbe(context.Background(), path); err == nil || wrap(err).Code != "conflict" {
		t.Fatalf("duplicate probe: %v", err)
	}
	must(t, os.Chmod(path, 0644))
	if readPrivateJSON(path, &req) == nil {
		t.Fatal("accepted non-private request")
	}
	link := filepath.Join(dir, "link.json")
	must(t, os.Symlink(path, link))
	if readPrivateJSON(link, &req) == nil {
		t.Fatal("accepted symlinked request")
	}
}
