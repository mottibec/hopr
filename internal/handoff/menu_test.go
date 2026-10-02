package handoff

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMenuLabelsKeepDestinationIdentityAndCancelSafely(t *testing.T) {
	runtime := &fakeRuntime{session: Session{Agent: "codex", ID: testID, CWD: "/work/project"}}
	e := &Engine{Config: Config{HostID: "work", Hosts: map[string]Host{
		"private-mac": {SSH: "workbox", ID: "private-mac", Label: "Home"},
		"other":       {SSH: "other", ID: "other"},
	}}, Runtime: runtime}
	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader("2\nclose\n"))
	to, err := menu(context.Background(), e, "w1:p1", "work-server", in, &out)
	must(t, err)
	if to != "private-mac" || !strings.Contains(out.String(), "2. Home (private-mac)") || !strings.Contains(out.String(), "work-server / w1:p1") {
		t.Fatalf("wrong identity or presentation: %q %s", to, out.String())
	}
	line, err := in.ReadString('\n')
	must(t, err)
	if line != "close\n" {
		t.Fatalf("menu swallowed subsequent input: %q", line)
	}
	for _, input := range []string{"", "\n", "3\n", "bad\n"} {
		if _, err := menu(context.Background(), e, "w1:p1", "work-server", strings.NewReader(input), &out); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if runtime.stops != 0 || runtime.launches != 0 {
		t.Fatal("picker changed agent lifecycle")
	}
}

func TestMenuKeepsConfigurationErrorVisible(t *testing.T) {
	var out, errout bytes.Buffer
	code := Main(context.Background(), []string{"menu", "--config", filepath.Join(t.TempDir(), "missing.json")}, strings.NewReader("\n"), &out, &errout)
	if code != 2 || !strings.Contains(out.String(), "configuration") || !strings.Contains(out.String(), "Press Enter to close") {
		t.Fatalf("code %d: %s %s", code, &out, &errout)
	}
}

func TestDestinationLabelsRejectTerminalControls(t *testing.T) {
	for _, label := range []string{"Home\nWrong", "Home\x1b[2J", strings.Repeat("x", 81)} {
		c := configFixture(t, "work", t.TempDir())
		c.Hosts["private"] = Host{SSH: "home", ID: "private", Label: label}
		if err := c.normalize(); err == nil {
			t.Fatalf("accepted unsafe label %q", label)
		}
	}
}
