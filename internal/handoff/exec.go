package handoff

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type limitedBuffer struct {
	bytes.Buffer
	Limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.Limit {
		return 0, fail("unsupported", "subprocess output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func run(ctx context.Context, dir string, env []string, input []byte, exe string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") && !strings.HasPrefix(item, "CLAUDE_CONFIG_DIR=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = bytes.NewReader(input)
	out := &limitedBuffer{Limit: MaxPackage}
	stderr := &limitedBuffer{Limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fail("dependency", "%s failed: %v: %s", exe, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

type SSH struct {
	Executable string
	Hosts      map[string]Host
}

func (s SSH) Call(ctx context.Context, to string, r Request) (Reply, error) {
	var reply Reply
	h, ok := s.Hosts[to]
	if !ok {
		return reply, fail("configuration", "unknown destination %s", to)
	}
	b, e := encodeRequest(r)
	if e != nil {
		return reply, e
	}
	// OpenSSH executes this fixed receiver command. No request value enters it.
	out, e := run(ctx, "", nil, b, s.Executable, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", h.SSH, `exec "$HOME/.local/bin/hopr" receive`)
	if e != nil {
		return reply, fail("uncertain", "SSH request failed; query/recover this move before any retry: %v", e)
	}
	if e = decodeStrict(out, &reply); e != nil {
		return reply, fail("uncertain", "invalid receiver response: %v", e)
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	if reply.Host != h.ID {
		return reply, fail("conflict", "destination host identity mismatch")
	}
	return reply, nil
}
func ReadRequest(r io.Reader) (Request, error) {
	var req Request
	b, e := io.ReadAll(io.LimitReader(r, 2*MaxPackage+1))
	if e != nil {
		return req, e
	}
	if len(b) > 2*MaxPackage {
		return req, fail("invalid_package", "request too large")
	}
	e = decodeStrict(b, &req)
	return req, e
}
