package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
)

// version is the release set, set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := cli.Options{Catalog: catalog.Embedded(), ReleaseSet: version, Out: os.Stdout}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		o.In = os.Stdin
	}
	err := cli.Run(ctx, os.Args[1:], o)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, cli.ErrUsage):
		fmt.Fprintln(os.Stderr, cli.Usage)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "kyquickstart:", err)
		return 1
	}
}
