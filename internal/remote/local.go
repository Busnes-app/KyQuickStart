package remote

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Local runs commands with sh on this machine. Tests use it in place of SSH.
type Local struct{}

func (Local) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	if ctx.Err() != nil {
		return out.Bytes(), ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), &ExitError{Code: ee.ExitCode(), Stderr: errb.Bytes()}
	}
	return out.Bytes(), err
}
