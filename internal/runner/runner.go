package runner

import (
	"context"
	"flag"
	"io"

	"github.com/Busnes-app/kyquickstart/internal/cli"
)

// Usage lists the commands that run inside the runner container.
const Usage = `runner commands (inside the runner container):
  serve --state DIR --socket PATH   answer KyYard on a private Unix socket
  serve-keygen --state DIR          create the runner's SSH key once; print its public line
  serve-import --state DIR          read a plan tar from stdin (kyquickstart handoff sends it)
  serve-export --state DIR          print the runner's stack.yaml (kyquickstart pull-plan reads it)`

// Main runs one runner command. o supplies the catalog and release set.
func Main(ctx context.Context, args []string, o cli.Options, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return cli.ErrUsage
	}
	fl := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fl.SetOutput(stderr)
	state := fl.String("state", "", "runner state directory")
	socket := fl.String("socket", "", "serve: Unix socket path")
	if err := fl.Parse(args[1:]); err != nil {
		return err
	}
	if fl.NArg() > 0 || *state == "" || (*socket != "") != (args[0] == "serve") {
		return cli.ErrUsage
	}
	switch args[0] {
	case "serve":
		s, err := newServer(*state, o, stderr)
		if err != nil {
			return err
		}
		l, err := listen(*socket)
		if err != nil {
			return err
		}
		s.log.Printf("runner %s serving on %s", s.id, *socket)
		return s.serve(ctx, l)
	case "serve-keygen", "serve-import", "serve-export":
		dir, err := openState(*state)
		if err != nil {
			return err
		}
		switch args[0] {
		case "serve-keygen":
			return keygen(dir, stdout)
		case "serve-import":
			return importPlan(dir, stdin)
		}
		return export(dir, stdout)
	}
	return cli.ErrUsage
}
