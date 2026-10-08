// Package remote runs shell commands on a target.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// Runner runs one POSIX shell command. A non-zero exit returns *ExitError, and stdout is
// returned either way.
type Runner interface {
	Run(ctx context.Context, cmd string, stdin []byte) (stdout []byte, err error)
}

// ExitError is a command that ran and exited non-zero.
type ExitError struct {
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	msg := bytes.TrimSpace(e.Stderr)
	if len(msg) > 2048 {
		msg = msg[len(msg)-2048:]
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, msg)
}

// Quote returns s as a single POSIX shell word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
