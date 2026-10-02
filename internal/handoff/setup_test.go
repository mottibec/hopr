package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	var out, errout bytes.Buffer
	args := []string{"--config", path, "setup", "--host-id", "work-mac", "--to", "workbox", "--remote-id", "private-mac", "--project", "app", "--path", dir, "--json"}
	if code := Main(context.Background(), args, nil, &out, &errout); code != 0 {
		t.Fatalf("%d %s %s", code, &out, &errout)
	}
	var output Output
	must(t, json.Unmarshal(out.Bytes(), &output))
	if !output.OK {
		t.Fatal(out.String())
	}
	c, err := LoadConfig(path)
	must(t, err)
	if c.HostID != "work-mac" || c.Hosts["private-mac"].SSH != "workbox" || c.Projects["app"].Path != dir {
		t.Fatalf("bad setup: %+v", c)
	}
	st, err := os.Stat(path)
	must(t, err)
	if st.Mode().Perm() != 0600 {
		t.Fatal("config is not private")
	}
	original, err := os.ReadFile(path)
	must(t, err)
	out.Reset()
	if code := Main(context.Background(), args, nil, &out, &errout); code != 4 {
		t.Fatalf("overwrite accepted: %d %s", code, &out)
	}
	after, err := os.ReadFile(path)
	must(t, err)
	if !bytes.Equal(original, after) {
		t.Fatal("existing config changed")
	}
}

func TestSetupInvalidDoesNotWrite(t *testing.T) {
	for _, args := range [][]string{{"--project", "app"}, {"--to", "bad;host"}, {"--backend", "unknown"}, {"--remote-id", "private"}, {"--host-id", "../bad"}} {
		path := filepath.Join(t.TempDir(), "config.json")
		var out bytes.Buffer
		if _, err := setup(path, args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("wrote invalid config")
		}
	}
}
