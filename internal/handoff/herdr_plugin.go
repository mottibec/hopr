package handoff

import (
	"context"
	"encoding/json"
	"os"
)

const herdrPluginID = "hopr"
const herdrSelectionEnv = "HOPR_HERDR_SELECTION"

type herdrSelection struct {
	ServerToken string        `json:"server_token"`
	Pane        string        `json:"pane"`
	Workspace   string        `json:"workspace"`
	Terminal    string        `json:"terminal"`
	Session     *agentSession `json:"session"`
}

func (h *Herdr) pluginServer(server string) error {
	if h.Config.Backend != "herdr" || os.Getenv("HERDR_PLUGIN_ID") != herdrPluginID {
		return fail("usage", "this entrypoint requires the Hopr Herdr plugin and herdr backend")
	}
	if socket := os.Getenv("HERDR_SOCKET_PATH"); socket == "" || socket != h.Config.Servers[server] {
		return fail("configuration", "plugin socket must match the configured source server")
	}
	return nil
}

func (h *Herdr) openPluginPopup(ctx context.Context, server, configPath string) error {
	if err := h.pluginServer(server); err != nil {
		return err
	}
	var invocation struct {
		Pane      string `json:"focused_pane_id"`
		Workspace string `json:"workspace_id"`
	}
	raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &invocation) != nil || invocation.Pane == "" || invocation.Workspace == "" {
		return fail("usage", "plugin action requires an explicit pane and workspace context")
	}
	token, err := h.token(server)
	if err != nil {
		return err
	}
	r, err := h.call(ctx, server, "pane.get", map[string]string{"pane_id": invocation.Pane})
	if err != nil {
		return err
	}
	if r.Pane.Pane != invocation.Pane || r.Pane.Workspace != invocation.Workspace || r.Pane.Terminal == "" {
		return fail("conflict", "plugin selection no longer matches the source pane")
	}
	selection := herdrSelection{token, r.Pane.Pane, r.Pane.Workspace, r.Pane.Terminal, r.Pane.Session}
	b, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	// Herdr 0.9.0 derives plugin popup context from current focus at launch.
	// Preserve the action's original selection separately across that async gap.
	_, err = h.call(ctx, server, "plugin.pane.open", map[string]any{
		"plugin_id": herdrPluginID, "entrypoint": "move", "placement": "popup",
		"env": map[string]string{herdrSelectionEnv: string(b), "HOPR_CONFIG": Expand(configPath)},
	})
	return err
}

func (h *Herdr) pluginMenuPane(ctx context.Context, server string) (string, error) {
	if err := h.pluginServer(server); err != nil {
		return "", err
	}
	if os.Getenv("HERDR_PLUGIN_ENTRYPOINT_ID") != "move" {
		return "", fail("usage", "open the Hopr plugin action to select a session")
	}
	var selection herdrSelection
	raw := os.Getenv(herdrSelectionEnv)
	if len(raw) > 16384 || decodeStrict([]byte(raw), &selection) != nil || selection.Pane == "" || selection.Terminal == "" || selection.Workspace == "" {
		return "", fail("usage", "missing or invalid captured plugin selection; invoke hopr.move")
	}
	token, err := h.token(server)
	if err != nil {
		return "", err
	}
	if token != selection.ServerToken {
		return "", fail("conflict", "plugin source server changed; reopen the popup")
	}
	r, err := h.call(ctx, server, "pane.get", map[string]string{"pane_id": selection.Pane})
	if err != nil {
		return "", err
	}
	a := r.Pane.Session
	b := selection.Session
	sameSession := a == nil && b == nil || a != nil && b != nil && *a == *b
	if r.Pane.Pane != selection.Pane || r.Pane.Terminal != selection.Terminal || r.Pane.Workspace != selection.Workspace || !sameSession {
		return "", fail("conflict", "plugin source pane or conversation changed; reopen the popup")
	}
	return selection.Pane, nil
}
