package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

const Version = "0.1.0"
const Protocol = 1
const MaxPackage = 256 << 20

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string                    { return e.Code + ": " + e.Message }
func fail(code, format string, args ...any) error { return &Error{code, fmt.Sprintf(format, args...)} }

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type Session struct {
	Host            string `json:"host"`
	Server          string `json:"server"`
	ServerToken     string `json:"server_token"`
	Pane            string `json:"pane"`
	Terminal        string `json:"terminal"`
	Agent           string `json:"agent"`
	ID              string `json:"id"`
	CWD             string `json:"cwd"`
	Project         string `json:"project"`
	NativePath      string `json:"native_path"`
	PID             int    `json:"pid"`
	ShellPID        int    `json:"shell_pid"`
	ProcessStart    string `json:"process_start"`
	AgentVersion    string `json:"agent_version"`
	WorkspacePolicy string `json:"workspace_policy"`
}

type File struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Kind   string `json:"kind"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}
type Workspace struct {
	HEAD      string `json:"head"`
	Branch    string `json:"branch"`
	Bundle    []byte `json:"bundle"`
	Staged    []byte `json:"staged"`
	Unstaged  []byte `json:"unstaged"`
	Files     []File `json:"files"`
	IndexTree string `json:"index_tree"`
	Digest    string `json:"digest"`
}
type Conversation struct {
	Agent  string   `json:"agent"`
	ID     string   `json:"id"`
	Data   []byte   `json:"data"`
	Paths  []string `json:"paths"`
	Memory []File   `json:"memory,omitempty"`
}
type Package struct {
	Protocol     int          `json:"protocol"`
	MoveID       string       `json:"move_id"`
	Source       Session      `json:"source"`
	Destination  string       `json:"destination"`
	Conversation Conversation `json:"conversation"`
	Workspace    Workspace    `json:"workspace"`
}
type Journal struct {
	SourceRetired     bool      `json:"source_retired"`
	Protocol          int       `json:"protocol"`
	ID                string    `json:"id"`
	Role              string    `json:"role"`
	State             string    `json:"state"`
	Intent            string    `json:"intent,omitempty"`
	Source            Session   `json:"source"`
	To                string    `json:"to"`
	Destination       string    `json:"destination"`
	TargetPath        string    `json:"target_path,omitempty"`
	TargetPane        string    `json:"target_pane,omitempty"`
	TargetTerminal    string    `json:"target_terminal,omitempty"`
	TargetServerToken string    `json:"target_server_token,omitempty"`
	PackageSHA        string    `json:"package_sha,omitempty"`
	Owner             string    `json:"owner"`
	Updated           time.Time `json:"updated"`
}
type Request struct {
	Protocol    int     `json:"protocol"`
	Action      string  `json:"action"`
	ID          string  `json:"id"`
	Source      Session `json:"source"`
	Destination string  `json:"destination"`
	Package     []byte  `json:"package,omitempty"`
	SHA256      string  `json:"sha256,omitempty"`
}
type Reply struct {
	Journal *Journal `json:"journal,omitempty"`
	Host    string   `json:"host,omitempty"`
	Error   *Error   `json:"error,omitempty"`
}

// Ports separate lifecycle effects from the journalled coordinator.
type Runtime interface {
	Preflight(context.Context, Session, bool) error
	Inspect(context.Context, string, string) (Session, error)
	Guard(context.Context, Session, bool) error
	Stop(context.Context, Session) error
	Stopped(context.Context, Session) (bool, error)
	Retire(context.Context, Session) error
	FindWorkspace(context.Context, string) (pane, terminal, token string, err error)
	CreateWorkspace(context.Context, string, string) (pane, terminal, token string, err error)
	Launch(context.Context, Journal) error
	Ready(context.Context, Journal) (bool, error)
}
type Transport interface {
	Call(context.Context, string, Request) (Reply, error)
}
