package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/runner"
)

// version is the release set, set at build time with -ldflags "-X main.version=...".
var version = "dev"

// runnerImage is the attested kyquickstart-runner image KyYard's pod runs beside it, pinned by
// digest, which a release build sets with -ldflags "-X main.runnerImage=...". Empty in source
// builds, which refuse to install KyYard.
var runnerImage = ""

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := cli.Options{Catalog: catalog.Embedded(), ReleaseSet: version, Out: os.Stdout, RunnerImage: runnerImage}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		o.In = os.Stdin
	}
	var err error
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "serve") {
		err = runner.Main(ctx, os.Args[1:], o, os.Stdin, os.Stdout, os.Stderr)
	} else {
		err = cli.Run(ctx, os.Args[1:], o)
	}
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, cli.ErrUsage):
		fmt.Fprintln(os.Stderr, cli.Usage+"\n\n"+runner.Usage)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "kyquickstart:", err)
		return 1
	}
}
