package dockerhost

import "context"

type call struct{ cmd, stdin string }

// fakeRunner records commands and answers through reply.
type fakeRunner struct {
	calls []call
	reply func(cmd string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, cmd string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, call{cmd, string(stdin)})
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(cmd)
}
