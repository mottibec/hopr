package handoff

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

type Engine struct {
	Config    Config
	Store     Store
	Runtime   Runtime
	Transport Transport
	Git       Git
	Native    Native
	// Fault is used by integration tests to model loss immediately after effects.
	Fault func(string) error
}

func New(c Config) *Engine {
	s := Store{c.StateDir}
	e := &Engine{Config: c, Store: s, Git: Git{c.Executables.Git}, Native: Native{c, s}, Transport: SSH{c.Executables.SSH, c.Hosts}}
	if c.Backend == "command" {
		e.Runtime = &CommandRuntime{Config: c}
	} else if c.Backend == "tmux" {
		e.Runtime = &Tmux{Config: c}
	} else {
		e.Runtime = &Herdr{Config: c}
	}
	return e
}
func (e *Engine) checkpoint(point string) error {
	if e.Fault != nil {
		return e.Fault(point)
	}
	return nil
}
func (e *Engine) save(j *Journal, state, intent string) error {
	j.State = state
	j.Intent = intent
	return e.Store.Save(j)
}
func (e *Engine) intent(j *Journal, action string) error {
	if err := e.save(j, j.State, action); err != nil {
		return err
	}
	return e.checkpoint("before_" + action)
}

type ownership struct {
	Move  string `json:"move"`
	Owner string `json:"owner"`
}

func (e *Engine) ownershipPath(s Session) string {
	return filepath.Join(e.Store.Root, "ownership", s.Agent+"-"+s.ID+".json")
}
func (e *Engine) owner(s Session) (ownership, error) {
	var o ownership
	b, err := os.ReadFile(e.ownershipPath(s))
	if os.IsNotExist(err) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	err = decodeStrict(b, &o)
	return o, err
}
func (e *Engine) setOwner(j *Journal, owner string) error {
	b, _ := json.Marshal(ownership{j.ID, owner})
	if err := atomicWrite(e.ownershipPath(j.Source), b, 0600); err != nil {
		return err
	}
	j.Owner = owner
	return nil
}

func (e *Engine) Move(ctx context.Context, pane, to, server, id string) (*Journal, error) {
	unlock, err := e.Store.Lock("coordinator")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if id != "" {
		j, err := e.Store.Load(id)
		if err == nil {
			if j.Role != "source" || j.Source.Pane != pane || j.Source.Server != server || j.To != to {
				return j, fail("conflict", "move UUID was used for a different request")
			}
			return j, e.advanceSource(ctx, j)
		}
		if er, ok := err.(*Error); !ok || er.Code != "not_found" {
			return nil, err
		}
		if !uuidPattern.MatchString(id) {
			return nil, fail("usage", "invalid move UUID")
		}
	}
	h, ok := e.Config.Hosts[to]
	if !ok {
		return nil, fail("configuration", "unknown host %s", to)
	}
	js, err := e.Store.Journals()
	if err != nil {
		return nil, err
	}
	for _, j := range js {
		if j.Role == "source" && j.State != "complete" && j.Source.Pane == pane && j.Source.Server == server {
			if j.To != to {
				return j, fail("conflict", "source pane already reserved by move %s", j.ID)
			}
			return j, e.advanceSource(ctx, j)
		}
	}
	s, err := e.Runtime.Inspect(ctx, pane, server)
	if err != nil {
		var prior *Journal
		for _, j := range js {
			if j.Role == "source" && j.Source.Pane == pane && j.Source.Server == server && j.To == to && j.State == "complete" && (prior == nil || j.Updated.After(prior.Updated)) {
				prior = j
			}
		}
		if prior != nil {
			return prior, fail("conflict", "selected pane cannot be inspected; previous move %s is complete. Use status with that UUID", prior.ID)
		}
		return nil, err
	}
	s.Host = e.Config.HostID
	if !uuidPattern.MatchString(s.ID) || s.CWD == "" || s.ServerToken == "" || s.Terminal == "" || s.PID <= 1 || s.ProcessStart == "" || (s.Agent != "codex" && s.Agent != "claude") {
		return nil, fail("unsupported", "adapter did not supply exact session identity and cwd")
	}
	s.Project, err = e.Config.ProjectFor(s.CWD)
	if err != nil {
		return nil, err
	}
	s.WorkspacePolicy = e.Config.Projects[s.Project].policy()
	for _, j := range js {
		if j.Role == "source" && j.Source.ServerToken == s.ServerToken && j.Source.Terminal == s.Terminal && j.Source.ID == s.ID {
			if j.To != to {
				return j, fail("conflict", "this source session already has move %s to %s", j.ID, j.To)
			}
			return j, e.advanceSource(ctx, j)
		}
	}
	o, err := e.owner(s)
	if err != nil {
		return nil, err
	}
	if o.Owner != "" && o.Owner != e.Config.HostID {
		return nil, fail("conflict", "session ownership is %s in move %s", o.Owner, o.Move)
	}
	if err = e.Runtime.Preflight(ctx, s, false); err != nil {
		return nil, err
	}
	paths, err := e.Native.Find(s)
	if err != nil {
		return nil, err
	}
	if s.NativePath == "" {
		for _, p := range paths {
			if s.Agent == "codex" || filepath.Base(filepath.Dir(p)) == encodeClaudePath(s.CWD) {
				if s.NativePath != "" {
					return nil, fail("conflict", "duplicate native session identity")
				}
				s.NativePath = p
			}
		}
		if s.NativePath == "" {
			return nil, fail("unsupported", "native session file is missing for selected project")
		}
	}
	if err = e.Runtime.Guard(ctx, s, false); err != nil {
		return nil, err
	}
	preview, err := e.Git.Snapshot(ctx, s.CWD, e.Config.Projects[s.Project].IncludeIgnored, "")
	if err != nil {
		return nil, err
	}
	for _, f := range preview.Files {
		if err = secretCheck(f.Data); err != nil {
			return nil, err
		}
	}
	// Validate identity, unsupported assets, and secret checks before stopping.
	if _, err = e.Native.Export(s); err != nil {
		return nil, err
	}
	if id == "" {
		id = UUID()
	}
	j := &Journal{Protocol: Protocol, ID: id, Role: "source", State: "prepared", Source: s, To: to, Destination: h.ID, Owner: e.Config.HostID}
	if err = e.Store.Save(j); err != nil {
		return j, err
	}
	return j, e.advanceSource(ctx, j)
}
func (e *Engine) request(j *Journal, action string) Request {
	return Request{Protocol: Protocol, Action: action, ID: j.ID, Source: j.Source, Destination: j.Destination}
}
func (e *Engine) advanceSource(ctx context.Context, j *Journal) error {
	if j.Role != "source" {
		return fail("usage", "not a source journal")
	}
	if j.State == "complete" {
		return nil
	}
	if j.State == "prepared" {
		if j.Intent == "stop" {
			stopped, err := e.Runtime.Stopped(ctx, j.Source)
			if err != nil {
				return err
			}
			if !stopped {
				return fail("uncertain", "stop was attempted; source still exists. Exit this exact agent manually and run recover. No stop is resent")
			}
		} else {
			if err := e.intent(j, "prepare_remote"); err != nil {
				return err
			}
			reply, err := e.Transport.Call(ctx, j.To, e.request(j, "prepare"))
			if err != nil {
				return err
			}
			if reply.Journal == nil || reply.Journal.State != "prepared" {
				return fail("conflict", "unexpected destination preparation state")
			}
			j.TargetPath = reply.Journal.TargetPath
			if err = e.checkpoint("after_prepare_remote"); err != nil {
				return err
			}
			if err = e.Runtime.Guard(ctx, j.Source, false); err != nil {
				return err
			}
			if err = e.intent(j, "stop"); err != nil {
				return err
			}
			if err = e.setOwner(j, "in_transit"); err != nil {
				return err
			}
			if err = e.Runtime.Stop(ctx, j.Source); err != nil {
				return err
			}
			if err = e.checkpoint("after_stop"); err != nil {
				return err
			}
			stopped, err := e.Runtime.Stopped(ctx, j.Source)
			if err != nil {
				return err
			}
			if !stopped {
				return fail("uncertain", "native process has not stopped; nothing exported")
			}
		}
		if err := e.save(j, "source_stopped", ""); err != nil {
			return err
		}
	}
	if j.State == "source_stopped" {
		if err := e.Runtime.Guard(ctx, j.Source, true); err != nil {
			return err
		}
		if err := e.intent(j, "snapshot"); err != nil {
			return err
		}
		packagePath := filepath.Join(e.Store.Dir(j.ID), "package.json")
		var b []byte
		var err error
		if j.PackageSHA != "" {
			b, err = readBounded(packagePath, MaxPackage)
			if err != nil {
				return err
			}
			if _, err = decodePackage(b, j.PackageSHA); err != nil {
				return err
			}
		} else {
			c, err := e.Native.Export(j.Source)
			if err != nil {
				return err
			}
			w, err := e.Git.Snapshot(ctx, j.Source.CWD, e.Config.Projects[j.Source.Project].IncludeIgnored, e.Store.Dir(j.ID))
			if err != nil {
				return err
			}
			for _, f := range w.Files {
				if err = secretCheck(f.Data); err != nil {
					return err
				}
			}
			if err = e.Runtime.Guard(ctx, j.Source, true); err != nil {
				return err
			}
			check, err := e.Git.Snapshot(ctx, j.Source.CWD, e.Config.Projects[j.Source.Project].IncludeIgnored, "")
			if err != nil {
				return err
			}
			again, err := e.Native.Export(j.Source)
			if err != nil {
				return err
			}
			if check.Digest != w.Digest || digest(again.Data) != digest(c.Data) {
				return fail("busy", "workspace or transcript changed while taking snapshot")
			}
			b, err = encodePackage(Package{Protocol: Protocol, MoveID: j.ID, Source: j.Source, Destination: j.Destination, Conversation: c, Workspace: w})
			if err != nil {
				return err
			}
			if err = atomicWrite(packagePath, b, 0600); err != nil {
				return err
			}
			j.PackageSHA = digest(b)
			if err = e.Store.Save(j); err != nil {
				return err
			}
		}
		if err = e.checkpoint("after_snapshot"); err != nil {
			return err
		}
		if !j.SourceRetired {
			if err = e.intent(j, "retire"); err != nil {
				return err
			}
			if err = e.Runtime.Retire(ctx, j.Source); err != nil {
				return err
			}
			if err = e.checkpoint("after_retire"); err != nil {
				return err
			}
			j.SourceRetired = true
			if err = e.Store.Save(j); err != nil {
				return err
			}
		}
		if err = e.intent(j, "copy"); err != nil {
			return err
		}
		req := e.request(j, "copy")
		req.Package = b
		req.SHA256 = j.PackageSHA
		if _, err = e.Transport.Call(ctx, j.To, req); err != nil {
			return err
		}
		if err = e.checkpoint("after_copy"); err != nil {
			return err
		}
		if err = e.save(j, "copied", ""); err != nil {
			return err
		}
	}
	// Always query the destination before driving it, including after lost SSH.
	stopped, err := e.Runtime.Stopped(ctx, j.Source)
	if err != nil {
		return err
	}
	if !stopped {
		return fail("conflict", "source is running again; refusing destination execution")
	}
	reply, err := e.Transport.Call(ctx, j.To, e.request(j, "status"))
	if err != nil {
		return err
	}
	remote := reply.Journal
	if remote == nil || remote.PackageSHA != j.PackageSHA {
		return fail("conflict", "destination journal/package mismatch")
	}
	if remote.State != "complete" {
		if err = e.intent(j, "advance_remote"); err != nil {
			return err
		}
		reply, err = e.Transport.Call(ctx, j.To, e.request(j, "advance"))
		if err != nil {
			return err
		}
		remote = reply.Journal
	}
	if remote == nil || remote.State != "complete" {
		return fail("uncertain", "target is not confirmed ready; source stays stopped")
	}
	if err = e.checkpoint("after_target_launch"); err != nil {
		return err
	}
	j.TargetPath = remote.TargetPath
	j.TargetPane = remote.TargetPane
	j.TargetTerminal = remote.TargetTerminal
	j.TargetServerToken = remote.TargetServerToken
	if err = e.setOwner(j, j.Destination); err != nil {
		return err
	}
	return e.save(j, "complete", "")
}

func (e *Engine) Receive(ctx context.Context, r Request) (Reply, error) {
	reply := Reply{Host: e.Config.HostID}
	if r.Protocol != Protocol || !uuidPattern.MatchString(r.ID) || r.Destination != e.Config.HostID {
		return reply, fail("invalid_package", "receiver protocol, move ID, or host mismatch")
	}
	unlock, err := e.Store.Lock("coordinator")
	if err != nil {
		return reply, err
	}
	defer unlock()
	j, loadErr := e.Store.Load(r.ID)
	if r.Action == "prepare" && loadErr != nil {
		if er, ok := loadErr.(*Error); !ok || er.Code != "not_found" {
			return reply, loadErr
		}
		if !uuidPattern.MatchString(r.Source.ID) || (r.Source.Agent != "codex" && r.Source.Agent != "claude") || !labelPattern.MatchString(r.Source.Host) || r.Source.Host == e.Config.HostID {
			return reply, fail("invalid_package", "invalid source identity")
		}
		if _, ok := e.Config.Projects[r.Source.Project]; !ok {
			return reply, fail("configuration", "destination has no project mapping %s", r.Source.Project)
		}
		if r.Source.WorkspacePolicy != e.Config.Projects[r.Source.Project].policy() {
			return reply, fail("configuration", "project include_ignored and claude_memory policies must match on both hosts")
		}
		allowed := false
		for _, h := range e.Config.Hosts {
			if h.ID == r.Source.Host {
				allowed = true
			}
		}
		if !allowed {
			return reply, fail("configuration", "source host is not configured on destination")
		}
		o, err := e.owner(r.Source)
		if err != nil {
			return reply, err
		}
		if o.Owner != "" && o.Owner != r.Source.Host {
			return reply, fail("conflict", "destination already reserves this session for %s (move %s)", o.Owner, o.Move)
		}
		if err = e.Runtime.Preflight(ctx, r.Source, true); err != nil {
			return reply, err
		}
		target := filepath.Join(e.Config.WorkspaceRoot, r.Source.Project, r.ID)
		if err = noSymlinks(target); err != nil {
			return reply, err
		}
		if _, err = os.Lstat(target); !os.IsNotExist(err) {
			return reply, fail("conflict", "destination checkout already exists")
		}
		j = &Journal{Protocol: Protocol, ID: r.ID, Role: "target", State: "prepared", Source: r.Source, Destination: e.Config.HostID, TargetPath: target, Owner: r.Source.Host}
		if err = e.Store.Save(j); err != nil {
			return reply, err
		}
		if err = e.setOwner(j, "in_transit"); err != nil {
			return reply, err
		}
		if err = e.Store.Save(j); err != nil {
			return reply, err
		}
	} else if loadErr != nil {
		return reply, loadErr
	}
	reply.Journal = j
	if j.Role != "target" || j.Source != r.Source || j.Destination != r.Destination {
		return reply, fail("conflict", "move request differs from destination journal")
	}
	switch r.Action {
	case "prepare", "status":
		return reply, nil
	case "copy":
		if j.PackageSHA != "" && j.PackageSHA != r.SHA256 {
			return reply, fail("conflict", "move package changed")
		}
		p, err := decodePackage(r.Package, r.SHA256)
		if err != nil {
			return reply, err
		}
		if p.MoveID != j.ID || p.Source != j.Source || p.Destination != j.Destination {
			return reply, fail("invalid_package", "package does not match prepared move")
		}
		if stateRank(j.State) >= stateRank("copied") {
			return reply, nil
		}
		if err = e.intent(j, "copy"); err != nil {
			return reply, err
		}
		if err = atomicWrite(filepath.Join(e.Store.Dir(j.ID), "package.json"), r.Package, 0600); err != nil {
			return reply, err
		}
		if err = e.checkpoint("after_receive_copy"); err != nil {
			return reply, err
		}
		j.PackageSHA = r.SHA256
		return reply, e.save(j, "copied", "")
	case "advance":
		return reply, e.advanceTarget(ctx, j)
	default:
		return reply, fail("usage", "unknown receiver action")
	}
}
func (e *Engine) advanceTarget(ctx context.Context, j *Journal) error {
	if j.State == "complete" {
		return nil
	}
	if stateRank(j.State) < stateRank("copied") {
		return fail("conflict", "package has not arrived")
	}
	b, err := readBounded(filepath.Join(e.Store.Dir(j.ID), "package.json"), MaxPackage)
	if err != nil {
		return err
	}
	p, err := decodePackage(b, j.PackageSHA)
	if err != nil {
		return err
	}
	if j.State == "copied" {
		if err = e.intent(j, "restore"); err != nil {
			return err
		}
		if _, err = os.Lstat(j.TargetPath); err == nil {
			w, err := e.Git.Snapshot(ctx, j.TargetPath, e.Config.Projects[j.Source.Project].IncludeIgnored, "")
			if err != nil {
				return err
			}
			if w.Digest != p.Workspace.Digest {
				return fail("conflict", "existing destination differs; it will not be overwritten")
			}
		} else if os.IsNotExist(err) {
			if err = e.Git.Restore(ctx, p.Workspace, j.TargetPath, e.Store.Dir(j.ID)); err != nil {
				return err
			}
		} else {
			return err
		}
		if err = e.checkpoint("after_restore"); err != nil {
			return err
		}
		if err = e.save(j, "restored", ""); err != nil {
			return err
		}
	}
	if j.State == "restored" {
		// Once launch intent exists, only observe. Never replay a possibly delivered launch.
		if j.Intent == "launch" {
			return e.finishTarget(ctx, j)
		}
		if j.Intent != "create_workspace" {
			if err = e.Runtime.Preflight(ctx, j.Source, true); err != nil {
				return err
			}
			planPath := filepath.Join(e.Store.Dir(j.ID), "import-plan.json")
			var plan ImportPlan
			if planBytes, er := os.ReadFile(planPath); er == nil {
				if err = decodeStrict(planBytes, &plan); err != nil {
					return err
				}
			} else if os.IsNotExist(er) {
				plan, err = e.Native.PlanImport(j.Source, p.Conversation, j.TargetPath)
				if err != nil {
					return err
				}
				pb, _ := json.Marshal(plan)
				if err = atomicWrite(planPath, pb, 0600); err != nil {
					return err
				}
			} else {
				return er
			}
			if err = e.intent(j, "import"); err != nil {
				return err
			}
			if err = ApplyImport(plan, filepath.Join(e.Store.Dir(j.ID), "native-backups")); err != nil {
				return err
			}
			if err = e.checkpoint("after_import"); err != nil {
				return err
			}
		}
		label := "hopr-" + j.ID
		if j.TargetPane == "" {
			if j.Intent == "create_workspace" {
				j.TargetPane, j.TargetTerminal, j.TargetServerToken, err = e.Runtime.FindWorkspace(ctx, label)
				if err != nil {
					return err
				}
				if j.TargetPane == "" {
					return fail("uncertain", "workspace creation outcome unknown; refusing to create a duplicate")
				}
			}
			if j.TargetPane == "" {
				if err = e.intent(j, "create_workspace"); err != nil {
					return err
				}
				j.TargetPane, j.TargetTerminal, j.TargetServerToken, err = e.Runtime.CreateWorkspace(ctx, label, j.TargetPath)
				if err != nil {
					return err
				}
				if err = e.checkpoint("after_create_workspace"); err != nil {
					return err
				}
			}
			if err = e.Store.Save(j); err != nil {
				return err
			}
		}
		if err = e.intent(j, "launch"); err != nil {
			return err
		}
		if err = e.setOwner(j, e.Config.HostID); err != nil {
			return err
		}
		if err = e.Runtime.Launch(ctx, *j); err != nil {
			return err
		}
		if err = e.checkpoint("after_launch"); err != nil {
			return err
		}
		return e.finishTarget(ctx, j)
	}
	if j.State == "target_ready" {
		return e.save(j, "complete", "")
	}
	return nil
}
func (e *Engine) finishTarget(ctx context.Context, j *Journal) error {
	ready, err := e.Runtime.Ready(ctx, *j)
	if err != nil {
		return err
	}
	if !ready {
		return fail("uncertain", "destination launch intent exists but exact session readiness is unconfirmed. Resolve its UI, then recover; no second launch is sent")
	}
	if err = e.save(j, "target_ready", ""); err != nil {
		return err
	}
	if err = e.checkpoint("after_target_ready"); err != nil {
		return err
	}
	return e.save(j, "complete", "")
}
func (e *Engine) Recover(ctx context.Context, id string) (*Journal, error) {
	unlock, err := e.Store.Lock("coordinator")
	if err != nil {
		return nil, err
	}
	defer unlock()
	j, err := e.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if j.Role == "source" {
		err = e.advanceSource(ctx, j)
	} else {
		err = e.advanceTarget(ctx, j)
	}
	return j, err
}
func encodeRequest(r Request) ([]byte, error) { return json.Marshal(r) }
