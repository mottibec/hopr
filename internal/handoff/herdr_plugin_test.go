package handoff

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func pluginEnvironment(t *testing.T, h *Herdr) {
	t.Helper()
	h.Config.Backend = "herdr"
	t.Setenv("HERDR_PLUGIN_ID", herdrPluginID)
	t.Setenv("HERDR_PLUGIN_ENTRYPOINT_ID", "move")
	t.Setenv("HERDR_SOCKET_PATH", h.Config.Servers["named"])
}

func TestHerdrPluginPreservesSelectionAcrossFocusChange(t *testing.T) {
	envs := make(chan map[string]string, 1)
	pane := paneInfo{Pane: "w1:p1", Workspace: "w1", Terminal: "term-selected", Session: &agentSession{Agent: "codex", Kind: "id", Value: testID, Source: "herdr:codex"}}
	h, _ := apiFixture(t, func(method string, raw json.RawMessage) any {
		switch method {
		case "pane.get":
			var p map[string]string
			must(t, json.Unmarshal(raw, &p))
			if p["pane_id"] != pane.Pane {
				t.Errorf("wrong pane: %v", p)
			}
			return map[string]any{"type": "pane_info", "pane": pane}
		case "plugin.pane.open":
			var p struct {
				PluginID   string            `json:"plugin_id"`
				Entrypoint string            `json:"entrypoint"`
				Placement  string            `json:"placement"`
				Env        map[string]string `json:"env"`
			}
			must(t, json.Unmarshal(raw, &p))
			if p.PluginID != "hopr" || p.Entrypoint != "move" || p.Placement != "popup" {
				t.Errorf("invalid popup request: %s", raw)
			}
			envs <- p.Env
			return map[string]any{"type": "ok"}
		default:
			t.Errorf("unexpected method: %s", method)
			return nil
		}
	})
	pluginEnvironment(t, h)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused_pane_id":"w1:p1","workspace_id":"w1","selected_text":"ignored"}`)
	config := filepath.Join(t.TempDir(), "config ' $(literal).json")
	must(t, h.openPluginPopup(context.Background(), "named", config))
	env := <-envs
	if env["HOPR_CONFIG"] != config {
		t.Fatalf("config path changed: %v", env)
	}
	t.Setenv(herdrSelectionEnv, env[herdrSelectionEnv])
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused_pane_id":"w2:p1","workspace_id":"w2"}`)
	t.Setenv("HERDR_ACTIVE_PANE_ID", "w2:p1")
	t.Setenv("TMUX_PANE", "%123")
	got, err := h.pluginMenuPane(context.Background(), "named")
	must(t, err)
	if got != pane.Pane {
		t.Fatalf("focus drift selected %s", got)
	}
}

func TestHerdrPluginRejectsStaleSelection(t *testing.T) {
	for _, bad := range []string{"missing", "invalid", "server", "pane", "workspace", "terminal", "session", "plugin", "socket", "entrypoint"} {
		t.Run(bad, func(t *testing.T) {
			p := paneInfo{Pane: "w1:p1", Workspace: "w1", Terminal: "term", Session: &agentSession{Agent: "codex", Kind: "id", Value: testID, Source: "herdr:codex"}}
			h, token := apiFixture(t, func(method string, _ json.RawMessage) any {
				if method != "pane.get" {
					t.Errorf("unexpected side effect: %s", method)
				}
				return map[string]any{"pane": p}
			})
			pluginEnvironment(t, h)
			s := herdrSelection{token, p.Pane, p.Workspace, p.Terminal, &agentSession{Agent: "codex", Kind: "id", Value: testID, Source: "herdr:codex"}}
			switch bad {
			case "server":
				s.ServerToken += "-old"
			case "pane":
				s.Pane = "old-pane"
			case "workspace":
				s.Workspace = "old-workspace"
			case "terminal":
				s.Terminal = "old-terminal"
			case "session":
				s.Session.Value = UUID()
			case "plugin":
				t.Setenv("HERDR_PLUGIN_ID", "another-plugin")
			case "socket":
				t.Setenv("HERDR_SOCKET_PATH", "/other-server.sock")
			case "entrypoint":
				t.Setenv("HERDR_PLUGIN_ENTRYPOINT_ID", "wrong")
			}
			raw, err := json.Marshal(s)
			must(t, err)
			if bad == "missing" {
				raw = nil
			} else if bad == "invalid" {
				raw = []byte(`{`)
			}
			t.Setenv(herdrSelectionEnv, string(raw))
			if _, err := h.pluginMenuPane(context.Background(), "named"); err == nil {
				t.Fatal("accepted stale plugin selection")
			}
		})
	}
}

func TestHerdrPluginRefusesMissingActionContext(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"focused_pane_id":"w1:p1"}`, `{"workspace_id":"w1"}`, `null`, `not-json`} {
		t.Run(raw, func(t *testing.T) {
			h, _ := apiFixture(t, func(method string, _ json.RawMessage) any {
				t.Errorf("missing context called API: %s", method)
				return nil
			})
			pluginEnvironment(t, h)
			t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", raw)
			if err := h.openPluginPopup(context.Background(), "named", "/config.json"); err == nil {
				t.Fatal("accepted incomplete selection")
			}
		})
	}
}
