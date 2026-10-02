package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func safeRelative(p string) error {
	if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\\\x00\r\n") {
		return fail("invalid_package", "unsafe relative path %q", p)
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || strings.EqualFold(s, ".git") {
			return fail("invalid_package", "forbidden path %q", p)
		}
	}
	return nil
}
func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func noSymlinks(path string) error {
	path = filepath.Clean(path)
	for p := path; p != "/" && p != "."; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return fail("conflict", "symlinked path component %s", p)
		}
	}
	return nil
}
func privateDir(path string) error {
	if err := noSymlinks(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	s, e := os.Stat(path)
	if e != nil {
		return e
	}
	if s.Mode().Perm()&0077 != 0 {
		return fail("configuration", "%s must be private (chmod 700)", path)
	}
	return nil
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	if err := noSymlinks(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type Store struct{ Root string }

func (s Store) Init() error          { return privateDir(s.Root) }
func (s Store) Dir(id string) string { return filepath.Join(s.Root, "moves", id) }
func (s Store) Load(id string) (*Journal, error) {
	if !uuidPattern.MatchString(id) {
		return nil, fail("usage", "invalid move UUID")
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "journal.json"))
	if os.IsNotExist(err) {
		return nil, fail("not_found", "move %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err = decodeStrict(b, &j); err != nil {
		return nil, err
	}
	if j.ID != id || j.Protocol != Protocol {
		return nil, fail("invalid_package", "invalid journal identity/version")
	}
	return &j, nil
}
func (s Store) Save(j *Journal) error {
	j.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.Dir(j.ID), "journal.json"), b, 0600)
}
func (s Store) Lock(name string) (func(), error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	p := filepath.Join(s.Root, "locks", digest([]byte(name)))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	if err := noSymlinks(p); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fail("busy", "another helper owns lock %s", name)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func (s Store) Journals() ([]*Journal, error) {
	entries, e := os.ReadDir(filepath.Join(s.Root, "moves"))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out []*Journal
	for _, v := range entries {
		if !v.IsDir() || !uuidPattern.MatchString(v.Name()) {
			continue
		}
		j, e := s.Load(v.Name())
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, nil
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e == nil && int64(len(b)) > limit {
		return nil, fail("unsupported", "%s exceeds size limit", path)
	}
	return b, e
}
func validateFiles(files []File) error {
	seen := map[string]bool{}
	kinds := map[string]string{}
	var total int
	for _, f := range files {
		if e := safeRelative(f.Path); e != nil {
			return e
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return fail("invalid_package", "duplicate/case-colliding path %s", f.Path)
		}
		seen[key] = true
		kinds[key] = f.Kind
		if f.Kind != "file" && f.Kind != "symlink" {
			return fail("invalid_package", "unsupported file type %s", f.Kind)
		}
		if f.Mode != 0644 && f.Mode != 0755 && f.Mode != 0600 {
			return fail("invalid_package", "unsupported mode for %s", f.Path)
		}
		if digest(f.Data) != f.SHA256 {
			return fail("invalid_package", "checksum mismatch: %s", f.Path)
		}
		total += len(f.Data)
		if total > MaxPackage {
			return fail("invalid_package", "files exceed size limit")
		}
		if f.Kind == "symlink" {
			target := string(f.Data)
			if target == "" || strings.ContainsRune(target, 0) || filepath.IsAbs(target) || !within("/root", filepath.Join("/root", filepath.Dir(f.Path), target)) {
				return fail("unsupported", "external symlink %s -> %q", f.Path, target)
			}
		}
	}
	for _, f := range files {
		for p := filepath.Dir(f.Path); p != "."; p = filepath.Dir(p) {
			if _, ok := kinds[strings.ToLower(p)]; ok {
				return fail("invalid_package", "file/symlink ancestor %s", p)
			}
		}
	}
	return nil
}
func materialize(root string, files []File) error {
	if e := validateFiles(files); e != nil {
		return e
	}
	for _, f := range files {
		p := filepath.Join(root, f.Path)
		if e := noSymlinks(filepath.Dir(p)); e != nil {
			return e
		}
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if _, e := os.Lstat(p); e == nil {
			return fail("conflict", "destination file exists: %s", p)
		} else if !os.IsNotExist(e) {
			return e
		}
		if f.Kind == "symlink" {
			if e := os.Symlink(string(f.Data), p); e != nil {
				return e
			}
		} else {
			if e := atomicWrite(p, f.Data, os.FileMode(f.Mode)); e != nil {
				return e
			}
		}
	}
	return nil
}
func encodePackage(p Package) ([]byte, error) {
	b, e := json.Marshal(p)
	if len(b) > MaxPackage {
		return nil, fail("unsupported", "package exceeds %d MiB", MaxPackage>>20)
	}
	return b, e
}
func decodePackage(b []byte, sha string) (Package, error) {
	var p Package
	if len(b) > MaxPackage || digest(b) != sha {
		return p, fail("invalid_package", "package size/checksum mismatch")
	}
	if e := decodeStrict(b, &p); e != nil {
		return p, fail("invalid_package", "%v", e)
	}
	if p.Protocol != Protocol || !uuidPattern.MatchString(p.MoveID) || !uuidPattern.MatchString(p.Source.ID) || p.Source.ID != p.Conversation.ID || p.Source.Agent != p.Conversation.Agent || p.Source.Host == p.Destination {
		return p, fail("invalid_package", "package identity/version mismatch")
	}
	if e := validateFiles(p.Workspace.Files); e != nil {
		return p, e
	}
	if e := validateFiles(p.Conversation.Memory); e != nil {
		return p, e
	}
	if e := validateConversation(p.Conversation); e != nil {
		return p, e
	}
	return p, nil
}
func stateRank(s string) int {
	for i, x := range []string{"prepared", "source_stopped", "copied", "restored", "target_ready", "complete"} {
		if s == x {
			return i
		}
	}
	return -1
}
func wrap(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{"internal", fmt.Sprint(err)}
}
