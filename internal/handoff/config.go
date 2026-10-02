package handoff

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Executables struct {
	Git    string `json:"git"`
	SSH    string `json:"ssh"`
	Herdr  string `json:"herdr"`
	Codex  string `json:"codex"`
	Claude string `json:"claude"`
	Lsof   string `json:"lsof"`
	Tmux   string `json:"tmux"`
}
type Host struct {
	SSH   string `json:"ssh"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}
type Project struct {
	Path           string     `json:"path"`
	IncludeIgnored []string   `json:"include_ignored"`
	ClaudeMemory   bool       `json:"claude_memory"`
	Requirements   [][]string `json:"requirements"`
}

func (p Project) policy() string {
	ignored := append([]string{}, p.IncludeIgnored...)
	sort.Strings(ignored)
	b, _ := json.Marshal(struct {
		Ignored []string
		Memory  bool
	}{ignored, p.ClaudeMemory})
	return digest(b)
}

type Config struct {
	Backend        string             `json:"backend"`
	AdapterCommand []string           `json:"adapter_command,omitempty"`
	HostID         string             `json:"host_id"`
	StateDir       string             `json:"state_dir"`
	WorkspaceRoot  string             `json:"workspace_root"`
	DefaultServer  string             `json:"default_server"`
	Servers        map[string]string  `json:"servers"`
	Hosts          map[string]Host    `json:"hosts"`
	Projects       map[string]Project `json:"projects"`
	Executables    Executables        `json:"executables"`
	CodexHome      string             `json:"codex_home"`
	ClaudeHome     string             `json:"claude_home"`
	TimeoutSeconds int                `json:"timeout_seconds"`
}

func DefaultConfigPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "hopr", "config.json")
}
func Expand(p string) string {
	h, _ := os.UserHomeDir()
	if p == "~" {
		return h
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(h, p[2:])
	}
	return p
}
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fail("configuration", "read %s: %v; run hopr setup to configure this Mac", path, err)
	}
	if err = decodeStrict(b, &c); err != nil {
		return c, fail("configuration", "%v", err)
	}
	return c, c.normalize()
}

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)
var sshPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,200}$`)

func (c *Config) normalize() error {
	if c.Backend == "" {
		c.Backend = "herdr"
	}
	if c.Backend != "herdr" && c.Backend != "tmux" && c.Backend != "command" {
		return fail("configuration", "backend must be herdr, tmux, or command")
	}
	if c.Backend == "command" && len(c.AdapterCommand) == 0 {
		return fail("configuration", "command backend requires adapter_command argv")
	}
	if !labelPattern.MatchString(c.HostID) {
		return fail("configuration", "host_id must be a stable unique label")
	}
	paths := []*string{&c.StateDir, &c.WorkspaceRoot, &c.CodexHome, &c.ClaudeHome}
	if c.StateDir == "" {
		c.StateDir = "~/.local/state/hopr"
	}
	if c.CodexHome == "" {
		c.CodexHome = "~/.codex"
	}
	if c.ClaudeHome == "" {
		c.ClaudeHome = "~/.claude"
	}
	for _, p := range paths {
		*p = Expand(*p)
		if !filepath.IsAbs(*p) || strings.ContainsAny(*p, "\x00\n\r") {
			return fail("configuration", "paths must be absolute: %q", *p)
		}
		*p = filepath.Clean(*p)
	}
	for k, p := range c.Servers {
		p = Expand(p)
		if !filepath.IsAbs(p) || !labelPattern.MatchString(k) {
			return fail("configuration", "invalid server %q", k)
		}
		c.Servers[k] = p
	}
	if c.Backend == "herdr" && c.Servers[c.DefaultServer] == "" {
		return fail("configuration", "default_server must name an explicit configured socket")
	}
	for name, h := range c.Hosts {
		if !labelPattern.MatchString(name) || !labelPattern.MatchString(h.ID) || !sshPattern.MatchString(h.SSH) || h.ID == c.HostID {
			return fail("configuration", "invalid destination %q", name)
		}
		if len(h.Label) > 80 || strings.IndexFunc(h.Label, unicode.IsControl) >= 0 {
			return fail("configuration", "destination %q label must be at most 80 bytes without control characters", name)
		}
	}
	for name, p := range c.Projects {
		p.Path = Expand(p.Path)
		if !labelPattern.MatchString(name) || !filepath.IsAbs(p.Path) {
			return fail("configuration", "invalid project %q", name)
		}
		if within(p.Path, c.StateDir) || within(p.Path, c.WorkspaceRoot) || within(c.WorkspaceRoot, p.Path) {
			return fail("configuration", "project, state directory, and destination workspace root must not overlap")
		}
		for _, f := range p.IncludeIgnored {
			if err := safeRelative(f); err != nil {
				return err
			}
		}
		c.Projects[name] = p
	}
	e := &c.Executables
	if e.Tmux == "" {
		e.Tmux = "tmux"
	}
	for _, d := range []struct {
		p *string
		v string
	}{{&e.Git, "git"}, {&e.SSH, "ssh"}, {&e.Herdr, "herdr"}, {&e.Codex, "codex"}, {&e.Claude, "claude"}, {&e.Lsof, "/usr/sbin/lsof"}} {
		if *d.p == "" {
			*d.p = d.v
		}
		*d.p = Expand(*d.p)
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 60
	}
	if c.Backend != "herdr" && c.DefaultServer == "" {
		c.DefaultServer = "default"
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 {
		return fail("configuration", "timeout_seconds must be 1..300")
	}
	return nil
}
func (c Config) Server(explicit string) (string, error) {
	if c.Backend != "herdr" {
		if explicit != "" {
			return explicit, nil
		}
		if c.DefaultServer != "" {
			return c.DefaultServer, nil
		}
		return "default", nil
	}
	if explicit != "" {
		if c.Servers[explicit] == "" {
			return "", fail("configuration", "unknown server %s", explicit)
		}
		return explicit, nil
	}
	if socket := os.Getenv("HERDR_SOCKET_PATH"); socket != "" {
		for name, p := range c.Servers {
			if p == socket {
				return name, nil
			}
		}
		return "", fail("configuration", "inherited HERDR_SOCKET_PATH is not a configured source server")
	}
	if os.Getenv("HERDR_SESSION") != "" {
		return "", fail("configuration", "use --server with HERDR_SESSION; pane IDs are server scoped")
	}
	return c.DefaultServer, nil
}
func (c Config) ProjectFor(path string) (string, error) {
	for name, p := range c.Projects {
		if filepath.Clean(path) == filepath.Clean(p.Path) {
			return name, nil
		}
	}
	// Managed checkouts are always directly below workspace_root/project/move UUID.
	rel, err := filepath.Rel(c.WorkspaceRoot, path)
	if err == nil {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 2 && uuidPattern.MatchString(parts[1]) {
			if _, ok := c.Projects[parts[0]]; ok {
				return parts[0], nil
			}
		}
	}
	return "", fail("unsupported", "workspace %s is not an exact configured project root or managed checkout", path)
}
func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fail("invalid_package", "trailing or invalid JSON")
	}
	return nil
}
