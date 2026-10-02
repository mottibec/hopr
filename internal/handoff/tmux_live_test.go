package handoff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealTmuxFixture(t *testing.T) {
	if os.Getenv("HOPR_TMUX_SMOKE") != "1" {
		t.Skip("set HOPR_TMUX_SMOKE=1 for an isolated real tmux socket")
	}
	c := configFixture(t, "fixture", "/private/tmp/unused-project")
	c.DefaultServer = "hopr-fixture-" + UUID()
	c.Executables.Tmux = "/opt/homebrew/bin/tmux"
	c.Executables.Claude = filepath.Join(t.TempDir(), "claude")
	capture := filepath.Join(t.TempDir(), "argv")
	write(t, c.Executables.Claude, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+capture+"'\nexec /bin/sleep 3600\n"))
	must(t, os.Chmod(c.Executables.Claude, 0755))
	runtime := &Tmux{c}
	ctx := context.Background()
	t.Cleanup(func() { runtime.cmd(ctx, c.DefaultServer, "kill-server") })
	label := "hopr-" + UUID()
	path := t.TempDir()
	pane, terminal, token, e := runtime.CreateWorkspace(ctx, label, path)
	must(t, e)
	found, term2, tok2, e := runtime.FindWorkspace(ctx, label)
	must(t, e)
	if pane != found || terminal != term2 || token != tok2 {
		t.Fatal("workspace reconciliation mismatch")
	}
	j := Journal{ID: UUID(), TargetPane: pane, TargetTerminal: terminal, TargetServerToken: token, TargetPath: path, Source: Session{Agent: "claude", ID: testID}}
	must(t, runtime.Launch(ctx, j))
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, e := os.ReadFile(capture)
		if e == nil {
			if strings.TrimSpace(string(b)) != "--resume\n"+testID {
				t.Fatalf("native argv %s", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake native executable not launched")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, e = runtime.Ready(ctx, j); e == nil {
		t.Fatal("tmux claimed readiness without operator attestation")
	}
	t.Log("Real tmux create/find/argv launch verified on isolated socket; native agent simulated")
}
