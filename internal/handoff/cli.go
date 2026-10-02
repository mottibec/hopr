package handoff

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const usage = `hopr — move a native agent conversation and Git workspace between Macs

Usage:
  hopr setup [--to SSH_ALIAS] [--host-id ID] [--remote-id ID]
             [--project NAME --path PATH] [--backend herdr|tmux] [--socket PATH]
  hopr doctor [--to HOST] [--json]
  hopr menu [--server NAME]
  hopr move --pane ID --to HOST [--server NAME] [--id UUID] [--json]
  hopr status UUID [--json]
  hopr recover UUID [--json]
  hopr attest --pane ID --agent codex|claude --session UUID [--server NAME]

Global: --config PATH, --json, --version, --help
attest is an explicit idle/readiness assertion for the tmux backend.
receive is a private stdin/stdout SSH protocol; never invoke it with untrusted data.
`

type Output struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch wrap(err).Code {
	case "usage", "configuration":
		return 2
	case "unsupported", "dependency", "authentication", "secrets", "action_required":
		return 3
	case "busy", "conflict", "invalid_package", "launch_rejected":
		return 4
	case "uncertain":
		return 5
	case "not_found":
		return 6
	default:
		return 1
	}
}
func Main(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) int {
	configPath := os.Getenv("HOPR_CONFIG")
	if configPath == "" {
		configPath = DefaultConfigPath()
	}
	jsonOutput := false
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i >= len(args) {
				fmt.Fprintln(errout, "--config requires a path")
				return 2
			}
			configPath = args[i]
		case "--json":
			jsonOutput = true
		case "--version":
			fmt.Fprintln(out, "hopr "+Version)
			return 0
		case "--help", "-h":
			fmt.Fprint(out, usage)
			return 0
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		fmt.Fprint(out, usage)
		return 2
	}
	if rest[0] == "setup" {
		result, err := setup(Expand(configPath), rest[1:], errout)
		return printOutput(out, jsonOutput, result, err)
	}
	if rest[0] == "auth-probe" {
		if len(rest) != 2 {
			return printOutput(out, true, nil, fail("usage", "auth-probe requires a private request file"))
		}
		return printOutput(out, true, nil, runAuthProbe(ctx, rest[1]))
	}
	if rest[0] == "menu" {
		in = bufio.NewReader(in)
	}
	c, err := LoadConfig(Expand(configPath))
	var result any
	if err == nil {
		e := New(c)
		command := rest[0]
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		fs.SetOutput(errout)
		pane := fs.String("pane", "", "exact pane ID")
		to := fs.String("to", "", "configured destination")
		server := fs.String("server", "", "explicit source server")
		id := fs.String("id", "", "idempotency UUID")
		agent := fs.String("agent", "", "native agent")
		session := fs.String("session", "", "exact native session UUID")
		plugin := fs.Bool("herdr-plugin", false, "use the plugin's captured source selection")
		err = fs.Parse(rest[1:])
		if err != nil {
			err = fail("usage", "%v", err)
		} else {
			switch command {
			case "herdr-popup":
				var srv string
				srv, err = c.Server(*server)
				if err == nil {
					err = (&Herdr{Config: c}).openPluginPopup(ctx, srv, configPath)
					result = map[string]string{"popup": "requested"}
				}
			case "doctor":
				result, err = e.Doctor(ctx, *to)
			case "receive":
				var reply Reply
				r, er := ReadRequest(in)
				if er == nil {
					if r.Action == "doctor" {
						if r.Protocol != Protocol || r.Destination != c.HostID {
							er = fail("configuration", "receiver identity mismatch")
						} else {
							_, er = e.Doctor(ctx, "")
							reply.Host = c.HostID
						}
					} else {
						reply, er = e.Receive(ctx, r)
					}
				}
				if er != nil {
					reply.Error = wrap(er)
				}
				json.NewEncoder(out).Encode(reply)
				return 0
			case "move", "menu", "attest":
				var srv string
				srv, err = c.Server(*server)
				if err != nil {
					break
				}
				if command == "menu" {
					if jsonOutput {
						err = fail("usage", "menu is interactive; use move --json")
						break
					}
					if *plugin {
						*pane, err = (&Herdr{Config: c}).pluginMenuPane(ctx, srv)
						if err != nil {
							break
						}
					} else {
						*pane = os.Getenv("HERDR_ACTIVE_PANE_ID")
						if *pane == "" {
							*pane = os.Getenv("TMUX_PANE")
						}
						if *pane == "" {
							err = fail("usage", "menu requires HERDR_ACTIVE_PANE_ID or TMUX_PANE")
							break
						}
					}
					*to, err = menu(ctx, e, *pane, srv, in, out)
					if err != nil {
						break
					}
					if *plugin {
						if _, err = (&Herdr{Config: c}).pluginMenuPane(ctx, srv); err != nil {
							break
						}
					}
				}
				if *pane == "" {
					err = fail("usage", "--pane is required")
					break
				}
				if command == "attest" {
					t, ok := e.Runtime.(*Tmux)
					if !ok {
						err = fail("usage", "attest applies only to tmux")
						break
					}
					err = t.Attest(ctx, *pane, srv, *agent, *session)
					result = map[string]string{"pane": *pane, "state": "operator_attested_idle"}
					break
				}
				if *to == "" {
					err = fail("usage", "--to is required")
					break
				}
				result, err = e.Move(ctx, *pane, *to, srv, *id)
			case "status", "recover":
				if fs.NArg() != 1 {
					err = fail("usage", "%s requires a move UUID", command)
					break
				}
				if command == "recover" {
					result, err = e.Recover(ctx, fs.Arg(0))
				} else {
					result, err = e.Status(ctx, fs.Arg(0))
				}
			default:
				err = fail("usage", "unknown command %s", command)
			}
		}
	}
	code := printOutput(out, jsonOutput, result, err)
	if rest[0] == "menu" && !jsonOutput {
		fmt.Fprint(out, "\nPress Enter to close: ")
		_, _ = bufio.NewReader(in).ReadString('\n')
	}
	return code
}
func printOutput(out io.Writer, jsonOutput bool, result any, err error) int {
	response := Output{OK: err == nil, Result: result}
	if err != nil {
		response.Error = wrap(err)
	}
	if jsonOutput {
		json.NewEncoder(out).Encode(response)
	} else {
		b, _ := json.MarshalIndent(response, "", "  ")
		fmt.Fprintln(out, string(b))
	}
	return ExitCode(err)
}
func menu(ctx context.Context, e *Engine, pane, server string, in io.Reader, out io.Writer) (string, error) {
	s, err := e.Runtime.Inspect(ctx, pane, server)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "%s / %s / %s\n%s %s\n%s\n\nMove to:\n", e.Config.HostID, server, pane, s.Agent, s.ID, s.CWD)
	var names []string
	for k := range e.Config.Hosts {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fail("configuration", "no destinations configured; add a host to the Hopr config")
	}
	for i, n := range names {
		label := e.Config.Hosts[n].Label
		if label == "" || label == n {
			label = n
		} else {
			label += " (" + n + ")"
		}
		fmt.Fprintf(out, "%d. %s\n", i+1, label)
	}
	fmt.Fprint(out, "Destination number (empty cancels): ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return "", fail("usage", "cancelled")
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(names) {
		return "", fail("usage", "cancelled or invalid destination")
	}
	fmt.Fprintf(out, "\nChecking this session and %s before stopping or transferring anything. Keep this popup open.\n", names[n-1])
	return names[n-1], nil
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (e *Engine) Doctor(ctx context.Context, to string) ([]Check, error) {
	if to != "" {
		h, ok := e.Config.Hosts[to]
		if !ok {
			return nil, fail("configuration", "unknown destination")
		}
		_, err := e.Transport.Call(ctx, to, Request{Protocol: Protocol, Action: "doctor", Destination: h.ID})
		return []Check{{"destination", err == nil, h.ID}}, err
	}
	checks := []Check{{"hopr", true, Version}, {"backend", true, e.Config.Backend}}
	var first error
	for _, a := range []string{"codex", "claude"} {
		v, err := agentVersion(ctx, e.Config, a)
		checks = append(checks, Check{a, err == nil, v})
		if err != nil {
			checks[len(checks)-1].Detail = err.Error()
			if first == nil {
				first = err
			}
		}
	}
	for name, cmd := range map[string][]string{"git": {e.Config.Executables.Git, "--version"}, "ssh": {e.Config.Executables.SSH, "-V"}} {
		_, err := run(ctx, "", nil, nil, cmd[0], cmd[1:]...)
		checks = append(checks, Check{name, err == nil, "available"})
		if err != nil {
			checks[len(checks)-1].Detail = err.Error()
			if first == nil {
				first = err
			}
		}
	}
	if h, ok := e.Runtime.(*Herdr); ok {
		r, err := h.call(ctx, e.Config.DefaultServer, "ping", map[string]any{})
		if err == nil && r.Protocol != 22 {
			err = fail("unsupported", "live Herdr protocol is %d", r.Protocol)
		}
		checks = append(checks, Check{"herdr", err == nil, fmt.Sprintf("protocol %d; %s", r.Protocol, r.Version)})
		if err != nil {
			checks[len(checks)-1].Detail = err.Error()
			if first == nil {
				first = err
			}
		}
	}
	for _, a := range []string{"codex", "claude"} {
		err := authReady(ctx, e.Config, a)
		checks = append(checks, Check{a + "_authentication", err == nil, "local credentials"})
		if err != nil {
			checks[len(checks)-1].Detail = err.Error()
			if first == nil {
				first = err
			}
		}
	}
	return checks, first
}
func (e *Engine) Status(ctx context.Context, id string) (any, error) {
	j, err := e.Store.Load(id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"local": j}
	if j.Role == "source" {
		reply, err := e.Transport.Call(ctx, j.To, e.request(j, "status"))
		if err != nil {
			result["destination"] = "unknown"
			return result, err
		}
		result["destination"] = reply.Journal
	}
	return result, nil
}
func authReady(ctx context.Context, c Config, agent string) error {
	if agent == "claude" && c.Backend == "herdr" {
		return (&Herdr{Config: c}).authReady(ctx, agent)
	}
	return authReadyDirect(ctx, c, agent)
}
func authReadyDirect(ctx context.Context, c Config, agent string) error {
	if agent == "codex" {
		if _, e := run(ctx, "", agentEnv(c), nil, c.Executables.Codex, "login", "status"); e != nil {
			return fail("authentication", "Codex login is not ready")
		}
		return nil
	}
	if agent != "claude" {
		return fail("unsupported", "unknown authentication adapter %s", agent)
	}
	b, e := run(ctx, "", agentEnv(c), nil, c.Executables.Claude, "auth", "status", "--json")
	if e != nil {
		return fail("authentication", "Claude login is not available in this launch environment; run Claude auth status there")
	}
	var a struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal(b, &a) != nil || !a.LoggedIn {
		return fail("authentication", "Claude authentication is not ready")
	}
	return nil
}
