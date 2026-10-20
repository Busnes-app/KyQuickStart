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
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/offboard"
)

const usage = "Usage: kyoffboard --config /private/runtime/config.json [--check | --init-tls | --healthcheck]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("kyoffboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("config", "", "private target configuration")
	check := flags.Bool("check", false, "validate without native requests or writes")
	initTLS := flags.Bool("init-tls", false, "create the bridge's key and self-signed leaf if both are absent")
	health := flags.Bool("healthcheck", false, "probe the local listener")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(out, usage)
		return 0
	}
	modes := 0
	for _, mode := range []bool{*check, *initTLS, *health} {
		if mode {
			modes++
		}
	}
	if err != nil || flags.NArg() != 0 || *file == "" || modes > 1 {
		fmt.Fprintln(diagnostics, usage)
		return 2
	}
	if *initTLS {
		if offboard.InitTLS(*file) != nil {
			fmt.Fprintln(diagnostics, offboard.ErrRuntime)
			return 1
		}
		return 0
	}
	runtime, err := offboard.LoadRuntime(*file)
	if err != nil {
		fmt.Fprintln(diagnostics, offboard.ErrRuntime)
		return 1
	}
	if *health {
		if runtime.Healthcheck(ctx) != nil {
			fmt.Fprintln(diagnostics, "kyoffboard: listener unhealthy")
			return 1
		}
		return 0
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
