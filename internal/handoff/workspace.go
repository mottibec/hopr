package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Git struct{ Executable string }

func (g Git) command(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.autocrlf=false", "-c", "diff.external=", "-c", "protocol.file.allow=always"}
	return run(ctx, dir, []string{"GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1"}, input, g.Executable, append(base, args...)...)
}
func (g Git) text(ctx context.Context, dir string, args ...string) (string, error) {
	b, e := g.command(ctx, dir, nil, args...)
	return strings.TrimSpace(string(b)), e
}
func (g Git) Check(ctx context.Context, root string) error {
	top, e := g.text(ctx, root, "rev-parse", "--show-toplevel")
	if e != nil {
		return e
	}
	resolved, e := filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	top, _ = filepath.EvalSymlinks(top)
	if top != resolved {
		return fail("unsupported", "agent cwd must be the Git workspace root")
	}
	if _, e = g.text(ctx, root, "rev-parse", "--verify", "HEAD^{commit}"); e != nil {
		return fail("unsupported", "unborn or invalid HEAD")
	}
	format, e := g.text(ctx, root, "rev-parse", "--show-object-format")
	if e != nil {
		return e
	}
	if format != "sha1" {
		return fail("unsupported", "Git object format %s is not supported; expected sha1", format)
	}
	shallow, e := g.text(ctx, root, "rev-parse", "--is-shallow-repository")
	if e != nil {
		return e
	}
	if shallow != "false" {
		return fail("unsupported", "shallow repositories are not supported")
	}
	gitdir, e := g.text(ctx, root, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return e
	}
	common, e := g.text(ctx, root, "rev-parse", "--git-common-dir")
	if e != nil {
		return e
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	for _, base := range []string{gitdir, common} {
		for _, p := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG", "index.lock", "shallow", "info/grafts", "objects/info/alternates"} {
			if _, e := os.Stat(filepath.Join(base, p)); e == nil {
				return fail("unsupported", "repository has %s", p)
			} else if !os.IsNotExist(e) {
				return e
			}
		}
	}
	cfg, e := g.text(ctx, root, "config", "--local", "--list")
	if e != nil {
		return e
	}
	for _, line := range strings.Split(cfg, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "filter.") || strings.Contains(lower, "sparsecheckout=true") || strings.Contains(lower, "partialclone") || strings.Contains(lower, "promisor=true") || strings.Contains(lower, "worktreeconfig=true") {
			return fail("unsupported", "unsupported repository configuration: %s", strings.Split(line, "=")[0])
		}
	}
	refs, e := g.text(ctx, root, "for-each-ref", "--format=%(refname)", "refs/replace", "refs/stash")
	if e != nil {
		return e
	}
	if refs != "" {
		return fail("unsupported", "replace refs and stashes must be resolved first")
	}
	index, e := g.command(ctx, root, nil, "ls-files", "--stage", "-z")
	if e != nil {
		return e
	}
	for _, entry := range bytes.Split(index, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta := bytes.SplitN(entry, []byte{'\t'}, 2)[0]
		f := strings.Fields(string(meta))
		if len(f) != 3 || f[2] != "0" {
			return fail("unsupported", "unresolved index conflict")
		}
		if f[0] == "160000" {
			return fail("unsupported", "Git submodules are not supported")
		}
	}
	flags, e := g.command(ctx, root, nil, "ls-files", "-v", "-z")
	if e != nil {
		return e
	}
	for _, f := range bytes.Split(flags, []byte{0}) {
		if len(f) > 0 && f[0] != 'H' {
			return fail("unsupported", "assume-unchanged, skip-worktree, or sparse index entry")
		}
	}
	debug, e := g.command(ctx, root, nil, "ls-files", "--debug")
	if e != nil {
		return e
	}
	if regexp.MustCompile(`flags: [1-9a-fA-F]`).Match(debug) {
		return fail("unsupported", "extended index flags (including intent-to-add) are not supported")
	}
	names, e := g.command(ctx, root, nil, "ls-files", "-z")
	if e != nil {
		return e
	}
	attrs, e := g.command(ctx, root, names, "check-attr", "--stdin", "-z", "filter", "working-tree-encoding")
	if e != nil {
		return e
	}
	a := bytes.Split(attrs, []byte{0})
	for i := 2; i < len(a); i += 3 {
		v := string(a[i])
		if v != "unspecified" && v != "unset" {
			return fail("unsupported", "Git LFS, clean/smudge filters, and working-tree-encoding are not supported (%s)", a[i-2])
		}
	}
	for _, p := range []string{".gitmodules", ".lfsconfig"} {
		if _, e := os.Stat(filepath.Join(root, p)); e == nil {
			return fail("unsupported", "%s is not supported", p)
		}
	}
	return nil
}
func (g Git) inventory(ctx context.Context, root string, ignored []string) ([]File, error) {
	names := map[string]bool{}
	for _, args := range [][]string{{"ls-files", "-z"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		b, e := g.command(ctx, root, nil, args...)
		if e != nil {
			return nil, e
		}
		for _, p := range bytes.Split(b, []byte{0}) {
			if len(p) > 0 {
				names[string(p)] = true
			}
		}
	}
	for _, p := range ignored {
		if e := safeRelative(p); e != nil {
			return nil, e
		}
		full := filepath.Join(root, p)
		if e := noSymlinks(filepath.Dir(full)); e != nil {
			return nil, e
		}
		if e := filepath.WalkDir(full, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			rel, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			names[rel] = true
			return nil
		}); e != nil {
			return nil, e
		}
	}
	var keys []string
	for p := range names {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	var out []File
	var total int
	for _, p := range keys {
		if e := safeRelative(p); e != nil {
			return nil, e
		}
		full := filepath.Join(root, p)
		if e := noSymlinks(filepath.Dir(full)); e != nil {
			return nil, e
		}
		s, e := os.Lstat(full)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		f := File{Path: p, Mode: 0644, Kind: "file"}
		if s.Mode()&0111 != 0 {
			f.Mode = 0755
		}
		if s.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(full)
			if e != nil {
				return nil, e
			}
			f.Kind = "symlink"
			f.Mode = 0644
			f.Data = []byte(target)
		} else if s.Mode().IsRegular() {
			if s.Size() > MaxPackage {
				return nil, fail("unsupported", "file too large: %s", p)
			}
			f.Data, e = readBounded(full, MaxPackage)
			if e != nil {
				return nil, e
			}
		} else {
			return nil, fail("unsupported", "nested repository/directory or special file: %s", p)
		}
		if bytes.HasPrefix(f.Data, []byte("version https://git-lfs.github.com/spec/v1")) {
			return nil, fail("unsupported", "Git LFS pointer: %s", p)
		}
		total += len(f.Data)
		if total > MaxPackage {
			return nil, fail("unsupported", "workspace exceeds package limit")
		}
		f.SHA256 = digest(f.Data)
		out = append(out, f)
	}
	return out, validateFiles(out)
}
func workspaceDigest(w Workspace) string {
	b, _ := json.Marshal(struct {
		Head, Branch, Index string
		Files               []File
	}{w.HEAD, w.Branch, w.IndexTree, w.Files})
	return digest(b)
}
func (g Git) Snapshot(ctx context.Context, root string, ignored []string, dir string) (Workspace, error) {
	var w Workspace
	if e := g.Check(ctx, root); e != nil {
		return w, e
	}
	var e error
	if w.HEAD, e = g.text(ctx, root, "rev-parse", "HEAD"); e != nil {
		return w, e
	}
	w.Branch, _ = g.text(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if w.IndexTree, e = g.text(ctx, root, "write-tree"); e != nil {
		return w, e
	}
	if w.Staged, e = g.command(ctx, root, nil, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "HEAD", "--"); e != nil {
		return w, e
	}
	if w.Unstaged, e = g.command(ctx, root, nil, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--"); e != nil {
		return w, e
	}
	if w.Files, e = g.inventory(ctx, root, ignored); e != nil {
		return w, e
	}
	w.Digest = workspaceDigest(w)
	if dir != "" {
		if e = os.MkdirAll(dir, 0700); e != nil {
			return w, e
		}
		f, e := os.CreateTemp(dir, "bundle-*")
		if e != nil {
			return w, e
		}
		p := f.Name()
		f.Close()
		defer os.Remove(p)
		os.Remove(p)
		if _, e = g.command(ctx, root, nil, "bundle", "create", p, "HEAD"); e != nil {
			return w, e
		}
		w.Bundle, e = readBounded(p, MaxPackage)
		if e != nil {
			return w, e
		}
	}
	return w, nil
}

var oidPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func (g Git) Restore(ctx context.Context, w Workspace, path, staging string) error {
	if !oidPattern.MatchString(w.HEAD) || !oidPattern.MatchString(w.IndexTree) || workspaceDigest(w) != w.Digest {
		return fail("invalid_package", "invalid Git object IDs or workspace digest")
	}
	if _, e := os.Lstat(path); e == nil {
		return fail("conflict", "destination already exists: %s", path)
	} else if !os.IsNotExist(e) {
		return e
	}
	if e := noSymlinks(path); e != nil {
		return e
	}
	if e := validateFiles(w.Files); e != nil {
		return e
	}
	// A partial restore is confined to private staging and is safe to discard.
	tmp, e := os.MkdirTemp(staging, "checkout-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(staging, "workspace.bundle")
	if e = atomicWrite(bundle, w.Bundle, 0600); e != nil {
		return e
	}
	if _, e = g.command(ctx, tmp, nil, "init", "--template="); e != nil {
		return e
	}
	if _, e = g.command(ctx, tmp, nil, "bundle", "verify", bundle); e != nil {
		return fail("invalid_package", "bundle verification: %v", e)
	}
	if _, e = g.command(ctx, tmp, nil, "fetch", "--no-tags", bundle, "HEAD"); e != nil {
		return e
	}
	got, e := g.text(ctx, tmp, "rev-parse", "FETCH_HEAD")
	if e != nil {
		return e
	}
	if got != w.HEAD {
		return fail("invalid_package", "bundle HEAD mismatch")
	}
	if w.Branch != "" {
		if _, e = g.command(ctx, tmp, nil, "check-ref-format", "--branch", w.Branch); e != nil {
			return fail("invalid_package", "invalid branch")
		}
		if _, e = g.command(ctx, tmp, nil, "symbolic-ref", "HEAD", "refs/heads/"+w.Branch); e != nil {
			return e
		}
	}
	refArgs := []string{"update-ref", "HEAD", w.HEAD}
	if w.Branch == "" {
		refArgs = []string{"update-ref", "--no-deref", "HEAD", w.HEAD}
	}
	if _, e = g.command(ctx, tmp, nil, refArgs...); e != nil {
		return e
	}
	if _, e = g.command(ctx, tmp, nil, "read-tree", "HEAD"); e != nil {
		return e
	}
	if len(w.Staged) > 0 {
		if _, e = g.command(ctx, tmp, w.Staged, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); e != nil {
			return fail("invalid_package", "staged patch: %v", e)
		}
	}
	tree, e := g.text(ctx, tmp, "write-tree")
	if e != nil {
		return e
	}
	if tree != w.IndexTree {
		return fail("invalid_package", "index tree mismatch")
	}
	if e = materialize(tmp, w.Files); e != nil {
		return e
	}
	// Validate the recorded unstaged patch against the reconstructed worktree.
	diff, e := g.command(ctx, tmp, nil, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--")
	if e != nil {
		return e
	}
	if !bytes.Equal(diff, w.Unstaged) {
		return fail("invalid_package", "unstaged changes differ after restore")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
