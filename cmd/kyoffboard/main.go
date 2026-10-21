package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/offboard"
)

const usage = "Usage: kyoffboard --config /private/runtime/config.json [--check]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, out, diagnostics io.Writer) int {
	if len(args) > 0 && strings.HasPrefix(args[0], "bookstack-") {
		return bookstack(ctx, args[0], args[1:], stdin, out, diagnostics)
	}
	flags := flag.NewFlagSet("kyoffboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("config", "", "private target configuration")
	check := flags.Bool("check", false, "validate without native requests or writes")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(out, usage)
		return 0
	}
	if err != nil || flags.NArg() != 0 || *file == "" {
		fmt.Fprintln(diagnostics, usage)
		return 2
	}
	runtime, err := offboard.LoadRuntime(*file)
	if err != nil {
		fmt.Fprintln(diagnostics, offboard.ErrRuntime)
		return 1
	}
	if *check {
		if json.NewEncoder(out).Encode(runtime.Report()) != nil {
			return 1
		}
		return 0
	}
	err = runtime.Serve(ctx)
	if errors.Is(err, context.Canceled) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(diagnostics, offboard.ErrRuntime)
		return 1
	}
	return 0
}
