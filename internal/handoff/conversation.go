package handoff

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Native struct {
	Config Config
	Store  Store
}

func (n Native) home(agent string) string {
	if agent == "codex" {
		return n.Config.CodexHome
	}
	return n.Config.ClaudeHome
}
func records(data []byte) ([]map[string]any, error) {
	if len(data) == 0 || len(data) > MaxPackage {
		return nil, fail("invalid_package", "empty/oversized conversation")
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 4096), 16<<20)
	var out []map[string]any
	for s.Scan() {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			return nil, fail("invalid_package", "blank transcript record")
		}
		d := json.NewDecoder(bytes.NewReader(s.Bytes()))
		d.UseNumber()
		var v map[string]any
		if e := d.Decode(&v); e != nil || v == nil {
			return nil, fail("invalid_package", "invalid JSONL record")
		}
		if _, e := d.Token(); e != io.EOF {
			return nil, fail("invalid_package", "trailing transcript data")
		}
		out = append(out, v)
	}
	if e := s.Err(); e != nil {
		return nil, fail("invalid_package", "transcript: %v", e)
	}
	return out, nil
}
func recordCWD(agent string, r map[string]any) (map[string]any, string) {
	if agent == "claude" {
		v, _ := r["cwd"].(string)
		return r, v
	}
	typ, _ := r["type"].(string)
	if typ == "session_meta" || typ == "turn_context" {
		p, _ := r["payload"].(map[string]any)
		v, _ := p["cwd"].(string)
		return p, v
	}
	return nil, ""
}
func validateConversation(c Conversation) error {
	if (c.Agent != "codex" && c.Agent != "claude") || !uuidPattern.MatchString(c.ID) {
		return fail("unsupported", "unsupported agent or invalid native session UUID")
	}
	if len(c.Paths) == 0 {
		return fail("invalid_package", "conversation has no workspace mapping")
	}
	for _, p := range c.Paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return fail("invalid_package", "invalid conversation workspace mapping")
		}
	}
	rows, e := records(c.Data)
	if e != nil {
		return e
	}
	identity := false
	for i, r := range rows {
		if c.Agent == "codex" && r["type"] == "session_meta" {
			p, _ := r["payload"].(map[string]any)
			if i != 0 || p["id"] != c.ID {
				return fail("invalid_package", "Codex session_meta identity mismatch")
			}
			identity = true
			if source, ok := p["source"].(map[string]any); ok {
				if _, ok := source["subagent"]; ok {
					return fail("unsupported", "Codex subagent session")
				}
			}
		}
		if c.Agent == "claude" {
			if id, ok := r["sessionId"].(string); ok {
				if id != c.ID {
					return fail("invalid_package", "Claude session identity mismatch")
				}
				identity = true
			}
			if r["isSidechain"] == true {
				return fail("unsupported", "Claude sidechain/subagent transcript")
			}
		}
		if _, cwd := recordCWD(c.Agent, r); cwd != "" {
			if !filepath.IsAbs(cwd) {
				return fail("unsupported", "nonabsolute transcript cwd")
			}
			if len(c.Paths) > 0 && !mappedPath(cwd, c.Paths, "$ROOT").mapped {
				return fail("unsupported", "transcript references another workspace cwd: %s", cwd)
			}
		}
		if hasExternalAsset(r) {
			return fail("unsupported", "transcript contains a file-backed image/asset; embed it or remove the dependency before moving")
		}
	}
	if !identity {
		return fail("invalid_package", "native session identity missing")
	}
	return nil
}
func hasExternalAsset(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		typ, _ := t["type"].(string)
		if typ == "local_image" || typ == "input_file" {
			return true
		}
		for k, x := range t {
			if s, ok := x.(string); ok && (k == "url" || k == "image_url" || k == "file_path") && strings.HasPrefix(s, "file:") {
				return true
			}
			if hasExternalAsset(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if hasExternalAsset(x) {
				return true
			}
		}
	}
	return false
}

type mapped struct {
	value  string
	mapped bool
}

func mappedPath(path string, roots []string, to string) mapped {
	// Longest prefix wins when a recorded workspace contains another alias.
	roots = append([]string(nil), roots...)
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, root := range roots {
		if within(root, path) {
			rel, _ := filepath.Rel(root, path)
			if rel == "." {
				return mapped{to, true}
			}
			return mapped{to + "/" + filepath.ToSlash(rel), true}
		}
	}
	return mapped{path, false}
}
func transform(c Conversation, to string) ([]byte, error) {
	rows, e := records(c.Data)
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	for _, r := range rows {
		if obj, cwd := recordCWD(c.Agent, r); cwd != "" {
			obj["cwd"] = mappedPath(cwd, c.Paths, to).value
		}
		b, e := json.Marshal(r)
		if e != nil {
			return nil, e
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
func prefixHistory(local, incoming Conversation) error {
	l, e := transform(local, "$HOPR_WORKSPACE")
	if e != nil {
		return e
	}
	r, e := transform(incoming, "$HOPR_WORKSPACE")
	if e != nil {
		return e
	}
	if !bytes.HasPrefix(r, l) {
		return fail("conflict", "conversation histories diverged or destination is ahead; existing session was preserved")
	}
	return nil
}
func (n Native) paths(s Session) []string {
	set := map[string]bool{s.CWD: true}
	if p, ok := n.Config.Projects[s.Project]; ok {
		set[p.Path] = true
	}
	js, _ := n.Store.Journals()
	for _, j := range js {
		if j.Source.Agent == s.Agent && j.Source.ID == s.ID {
			set[j.Source.CWD] = true
			if j.TargetPath != "" {
				set[j.TargetPath] = true
			}
		}
	}
	var out []string
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func (n Native) Find(s Session) ([]string, error) {
	var paths []string
	root := filepath.Join(n.home(s.Agent), "sessions")
	if s.Agent == "claude" {
		root = filepath.Join(n.home(s.Agent), "projects")
	}
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) && path == root {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fail("unsupported", "symlink inside native session storage: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		match := name == s.ID+".jsonl"
		if s.Agent == "codex" {
			match = strings.HasSuffix(name, "-"+s.ID+".jsonl")
		}
		if match {
			paths = append(paths, path)
		}
		if strings.Contains(name, s.ID) && strings.HasSuffix(name, ".jsonl.zst") {
			return fail("unsupported", "compressed native transcripts are not supported")
		}
		return nil
	})
	return paths, e
}
func (n Native) Export(s Session) (Conversation, error) {
	c := Conversation{Agent: s.Agent, ID: s.ID, Paths: n.paths(s)}
	paths, e := n.Find(s)
	if e != nil {
		return c, e
	}
	if s.NativePath != "" {
		found := false
		for _, p := range paths {
			if p == s.NativePath {
				paths = []string{p}
				found = true
				break
			}
		}
		if !found {
			return c, fail("conflict", "selected native transcript is missing")
		}
	}
	if len(paths) > 1 && s.Agent == "claude" {
		want := filepath.Join(n.home(s.Agent), "projects", encodeClaudePath(s.CWD), s.ID+".jsonl")
		for _, p := range paths {
			if p == want {
				paths = []string{p}
				break
			}
		}
	}
	if len(paths) != 1 {
		return c, fail("unsupported", "expected exactly one native transcript for %s; found %d", s.ID, len(paths))
	}
	if c.Data, e = readBounded(paths[0], MaxPackage); e != nil {
		return c, e
	}
	if e = validateConversation(c); e != nil {
		return c, e
	}
	if s.Agent == "claude" {
		side := strings.TrimSuffix(paths[0], ".jsonl")
		if entries, e := os.ReadDir(side); e == nil && len(entries) > 0 {
			return c, fail("unsupported", "Claude session has auxiliary/subagent state at %s", side)
		}
		if n.Config.Projects[s.Project].ClaudeMemory {
			c.Memory, e = memoryFiles(filepath.Join(filepath.Dir(paths[0]), "memory"))
			if e != nil {
				return c, e
			}
		}
	}
	if e = secretCheck(c.Data); e != nil {
		return c, e
	}
	for _, f := range c.Memory {
		if e = secretCheck(f.Data); e != nil {
			return c, e
		}
	}
	return c, nil
}
func encodeClaudePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
func memoryFiles(root string) ([]File, error) {
	var out []File
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) && path == root {
			return nil
		}
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fail("unsupported", "nonregular memory file")
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		b, e := readBounded(path, MaxPackage)
		if e != nil {
			return e
		}
		out = append(out, File{rel, 0600, "file", b, digest(b)})
		return nil
	})
	return out, e
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`),
	regexp.MustCompile(`(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|xox[baprs]-[A-Za-z0-9-]{20,})`),
	regexp.MustCompile(`(?i)(?:api[_-]?key|secret[_-]?key|password|access[_-]?token)\s*[=:]\s*["']?[A-Za-z0-9_+/=-]{16,}`),
}

func secretCheck(b []byte) error {
	for _, p := range secretPatterns {
		if p.Match(b) {
			return fail("secrets", "likely credential in transfer data; no bytes or matched value were logged; remove it before moving")
		}
	}
	return nil
}

type ImportFile struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	Data   []byte `json:"data"`
}
type ImportPlan struct {
	Files []ImportFile `json:"files"`
}

func (n Native) PlanImport(s Session, c Conversation, target string) (ImportPlan, error) {
	var plan ImportPlan
	if e := validateConversation(c); e != nil {
		return plan, e
	}
	paths, e := n.Find(s)
	if e != nil {
		return plan, e
	}
	roots := append(n.paths(s), c.Paths...)
	var existingPath string
	var existing []byte
	if s.Agent == "codex" && len(paths) > 1 {
		return plan, fail("conflict", "duplicate Codex native session IDs")
	}
	for _, path := range paths {
		b, e := readBounded(path, MaxPackage)
		if e != nil {
			return plan, e
		}
		local := Conversation{Agent: s.Agent, ID: s.ID, Data: b, Paths: roots}
		if e = validateConversation(local); e != nil {
			return plan, e
		}
		if e = prefixHistory(local, c); e != nil {
			return plan, e
		}
		if s.Agent == "codex" {
			existingPath = path
			existing = b
		}
	}
	data, e := transform(c, target)
	if e != nil {
		return plan, e
	}
	path := filepath.Join(n.home(s.Agent), "projects", encodeClaudePath(target), s.ID+".jsonl")
	if s.Agent == "codex" {
		path = existingPath
		if path == "" {
			rows, err := records(c.Data)
			if err != nil {
				return plan, err
			}
			stamp, _ := rows[0]["timestamp"].(string)
			created, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return plan, fail("unsupported", "Codex session_meta has no valid creation timestamp")
			}
			path = filepath.Join(n.home(s.Agent), "sessions", created.Format("2006/01/02"), "rollout-"+created.Format("2006-01-02T15-04-05")+"-"+s.ID+".jsonl")
		}
		if len(existing) > 0 {
			// Keep the existing prefix byte-for-byte. Only new records are appended;
			// Codex's --cd supplies the new workspace when session_meta is older.
			oldRows, e := records(existing)
			if e != nil {
				return plan, e
			}
			newLines := bytes.SplitAfter(data, []byte{'\n'})
			var b bytes.Buffer
			b.Write(existing)
			if existing[len(existing)-1] != '\n' {
				b.WriteByte('\n')
			}
			for _, l := range newLines[len(oldRows):] {
				b.Write(l)
			}
			data = b.Bytes()
		}
	}
	before := ""
	if b, e := os.ReadFile(path); e == nil {
		before = digest(b)
	} else if !os.IsNotExist(e) {
		return plan, e
	}
	plan.Files = append(plan.Files, ImportFile{path, before, data})
	if len(c.Memory) > 0 && !n.Config.Projects[s.Project].ClaudeMemory {
		return plan, fail("unsupported", "destination project has not enabled Claude memory import")
	}
	for _, f := range c.Memory {
		p := filepath.Join(filepath.Dir(path), "memory", f.Path)
		before := ""
		if b, e := os.ReadFile(p); e == nil {
			if !bytes.Equal(b, f.Data) {
				return plan, fail("conflict", "Claude memory differs: %s", f.Path)
			}
			before = digest(b)
		} else if !os.IsNotExist(e) {
			return plan, e
		}
		plan.Files = append(plan.Files, ImportFile{p, before, f.Data})
	}
	return plan, nil
}
func ApplyImport(plan ImportPlan, backup string) error {
	// Validate every destination before any write; recovery accepts only the
	// recorded preimage or the exact intended result.
	for _, f := range plan.Files {
		if e := noSymlinks(f.Path); e != nil {
			return e
		}
		b, e := os.ReadFile(f.Path)
		if os.IsNotExist(e) {
			if f.Before != "" {
				return fail("conflict", "native file disappeared: %s", f.Path)
			}
			continue
		}
		if e != nil {
			return e
		}
		h := digest(b)
		if h != f.Before && h != digest(f.Data) {
			return fail("conflict", "native file changed during import: %s", f.Path)
		}
	}
	for _, f := range plan.Files {
		b, e := os.ReadFile(f.Path)
		if e == nil && digest(b) == digest(f.Data) {
			continue
		}
		if e == nil && f.Before != "" {
			bp := filepath.Join(backup, digest([]byte(f.Path))+".jsonl")
			if e = atomicWrite(bp, b, 0600); e != nil {
				return e
			}
		}
		if e = atomicWrite(f.Path, f.Data, 0600); e != nil {
			return e
		}
	}
	return nil
}
