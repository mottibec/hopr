package handoff

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func setup(path string, args []string, errout io.Writer) (any, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(errout)
	hostID := fs.String("host-id", strings.TrimSuffix(strings.ToLower(hostname), ".local"), "stable local host ID")
	to := fs.String("to", "", "destination SSH alias")
	remoteID := fs.String("remote-id", "", "destination's configured host_id; defaults to SSH alias")
	backend := fs.String("backend", "herdr", "herdr or tmux")
	socket := fs.String("socket", os.Getenv("HERDR_SOCKET_PATH"), "explicit local Herdr socket")
	project := fs.String("project", "", "shared project key")
	projectPath := fs.String("path", "", "local Git project root")
	if err := fs.Parse(args); err != nil {
		return nil, fail("usage", "%v", err)
	}
	if fs.NArg() != 0 || (*project == "") != (*projectPath == "") {
		return nil, fail("usage", "setup takes flags only; use --project NAME together with --path PATH")
	}
	if *backend != "herdr" && *backend != "tmux" {
		return nil, fail("usage", "setup supports herdr or tmux; configure command adapters in JSON")
	}
	if *remoteID != "" && *to == "" {
		return nil, fail("usage", "--remote-id requires --to")
	}
	if *remoteID == "" {
		*remoteID = *to
	}
	if *socket == "" {
		*socket = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	c := Config{
		HostID: *hostID, Backend: *backend, DefaultServer: "default",
		WorkspaceRoot: filepath.Join(home, "HoprWorkspaces"),
		Servers:       map[string]string{"default": *socket},
		Hosts:         map[string]Host{}, Projects: map[string]Project{},
	}
	if *to != "" {
		c.Hosts[*remoteID] = Host{SSH: *to, ID: *remoteID}
	}
	if *project != "" {
		p, err := filepath.Abs(Expand(*projectPath))
		if err != nil {
			return nil, err
		}
		p, err = filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fail("configuration", "project path: %v", err)
		}
		c.Projects[*project] = Project{Path: p, IncludeIgnored: []string{}}
	}
	if err = c.normalize(); err != nil {
		return nil, err
	}
	for _, executable := range []*string{&c.Executables.Git, &c.Executables.SSH, &c.Executables.Herdr, &c.Executables.Codex, &c.Executables.Claude, &c.Executables.Tmux} {
		if found, err := exec.LookPath(*executable); err == nil {
			*executable = found
		}
	}
	if err = noSymlinks(path); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil, fail("conflict", "configuration already exists at %s; edit it explicitly, setup never overwrites", path)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(c); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return nil, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	next := []string{"Run hopr doctor on both Macs; each needs its own agent login.", "Add the optional Herdr popup binding from examples/herdr.toml yourself."}
	if *project == "" {
		next = append([]string{"Add the same project key to projects on both Macs, with each Mac's local Git root."}, next...)
	}
	return map[string]any{"config": path, "host_id": c.HostID, "destinations": c.Hosts, "next": next}, nil
}
