package handoff

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

type authProbeRequest struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	Workspace string `json:"workspace"`
	Config    Config `json:"config"`
}

type authProbeResult struct {
	ID    string `json:"id"`
	Error *Error `json:"error,omitempty"`
}

func readPrivateJSON(path string, v any) error {
	if err := noSymlinks(path); err != nil {
		return err
	}
	s, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 || s.Size() > 1<<20 {
		return fail("configuration", "invalid private authentication probe file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	return decodeStrict(b, v)
}

func runAuthProbe(ctx context.Context, path string) error {
	var req authProbeRequest
	if err := readPrivateJSON(path, &req); err != nil {
		return err
	}
	if !uuidPattern.MatchString(req.ID) || req.Agent != "claude" || req.Config.Backend != "herdr" {
		return fail("configuration", "invalid authentication probe request")
	}
	if err := req.Config.normalize(); err != nil {
		return err
	}
	dir := filepath.Join(req.Config.StateDir, "auth-probes", req.ID)
	if path != filepath.Join(dir, "request.json") || req.Workspace == "" ||
		os.Getenv("HERDR_WORKSPACE_ID") != req.Workspace ||
		os.Getenv("HERDR_SOCKET_PATH") != req.Config.Servers[req.Config.DefaultServer] {
		return fail("configuration", "authentication probe must run in its selected Herdr workspace")
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail("conflict", "authentication probe has already started: %v", err)
	}
	f.Close()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := authProbeResult{ID: req.ID}
	if err := authReadyDirect(ctx, req.Config, req.Agent); err != nil {
		result.Error = wrap(err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "result.json"), b, 0600)
}

// A macOS Keychain login available to Herdr may be unavailable to SSH. Run
// the check where the agent will launch, returning only a nonce and status.
func (h *Herdr) authReady(ctx context.Context, agent string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(h.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	server := h.Config.DefaultServer
	token, err := h.token(server)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	id := UUID()
	dir := filepath.Join(h.Config.StateDir, "auth-probes", id)
	if err = privateDir(dir); err != nil {
		return err
	}
	req := authProbeRequest{ID: id, Agent: agent, Config: h.Config}
	requestPath := filepath.Join(dir, "request.json")
	save := func() error {
		b, e := json.Marshal(req)
		if e != nil {
			return e
		}
		return atomicWrite(requestPath, b, 0600)
	}
	if err = save(); err != nil {
		return err
	}
	r, err := h.call(ctx, server, "workspace.create", map[string]any{"cwd": dir, "label": "hopr-auth-" + id, "focus": false})
	if err != nil {
		return err
	}
	req.Workspace = r.Workspace.ID
	if req.Workspace == "" {
		return fail("uncertain", "authentication workspace identity missing; inspect %s", dir)
	}
	if err = save(); err != nil {
		return err
	}
	_, err = h.call(ctx, server, "layout.apply", map[string]any{
		"workspace_id": req.Workspace, "tab_label": "Hopr login check", "focus": false,
		"root": map[string]any{"type": "pane", "cwd": dir, "command": []string{exe, "auth-probe", requestPath}},
	})
	if err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var result authProbeResult
		err = readPrivateJSON(filepath.Join(dir, "result.json"), &result)
		if err == nil {
			if result.ID != id {
				return fail("conflict", "authentication probe identity mismatch")
			}
			current, e := h.token(server)
			if e != nil || current != token {
				return fail("uncertain", "Herdr server changed during authentication check")
			}
			if e = h.closeAuthProbe(ctx, req); e != nil {
				return e
			}
			if e = os.RemoveAll(dir); e != nil {
				return e
			}
			if result.Error != nil {
				return result.Error
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fail("authentication", "Herdr login check did not finish; inspect diagnostic workspace %s and %s; handoff has not advanced past this check", req.Workspace, dir)
		case <-ticker.C:
		}
	}
}

func (h *Herdr) closeAuthProbe(ctx context.Context, req authProbeRequest) error {
	dir := filepath.Join(req.Config.StateDir, "auth-probes", req.ID)
	// The result is committed just before the helper exits. Wait for its pane
	// to return to the shell; never close a pane occupied by another process.
	for {
		r, err := h.call(ctx, h.Config.DefaultServer, "pane.list", map[string]any{"workspace_id": req.Workspace})
		if err != nil {
			return err
		}
		if len(r.Panes) > 2 {
			return fail("uncertain", "authentication workspace has new panes; refusing cleanup")
		}
		idle := true
		for _, p := range r.Panes {
			if p.CWD != dir || p.Agent != "" {
				return fail("uncertain", "authentication pane changed; refusing cleanup")
			}
			info, err := h.call(ctx, h.Config.DefaultServer, "pane.process_info", map[string]any{"pane_id": p.Pane})
			if err != nil {
				return err
			}
			for _, proc := range info.ProcessInfo.Processes {
				if proc.PID != info.ProcessInfo.ShellPID {
					idle = false
				}
			}
		}
		if idle {
			break
		}
		select {
		case <-ctx.Done():
			return fail("uncertain", "authentication pane is still occupied; refusing cleanup")
		case <-time.After(100 * time.Millisecond):
		}
	}
	r, err := h.call(ctx, h.Config.DefaultServer, "workspace.list", map[string]any{})
	if err != nil {
		return err
	}
	for _, w := range r.Workspaces {
		if w.ID == req.Workspace && w.Label == "hopr-auth-"+req.ID {
			_, err = h.call(ctx, h.Config.DefaultServer, "workspace.close", map[string]any{"workspace_id": req.Workspace})
			return err
		}
	}
	return fail("uncertain", "authentication workspace changed; refusing cleanup")
}
