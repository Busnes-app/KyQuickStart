package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/stockbookstack"
)

// The catalog BookStack's receiver: its defaults are that Compose file's paths and names.
const bookstackUsage = `Usage: kyoffboard bookstack-directory [--probe]   serve KyIdentity's signed directory events
       kyoffboard bookstack-bootstrap --bookstack URL --token-file F   (create-admin's password on stdin)
       kyoffboard bookstack-check     the token works and belongs to the service account
       kyoffboard bookstack-roles [--check]   map the admin role to the admin claim
flags: --bookstack URL --token-file F --secret-file F --listen ADDR --admin-claim R --user-claim R`

// bookstack runs a bookstack-* subcommand. Diagnostics name no flag value, path or token.
func bookstack(ctx context.Context, cmd string, args []string, stdin io.Reader, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("bookstack", "http://bookstack", "")
	tokenFile := flags.String("token-file", "/token/bookstack_token", "")
	secretFile := flags.String("secret-file", "/run/secrets/directory_secret", "")
	listen := flags.String("listen", ":8080", "")
	adminClaim := flags.String("admin-claim", stockbookstack.AdminClaim, "")
	userClaim := flags.String("user-claim", stockbookstack.UserClaim, "")
	check := flags.Bool("check", false, "")
	probe := flags.Bool("probe", false, "")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(out, bookstackUsage)
		return 0
	}
	known := cmd == "bookstack-directory" || cmd == "bookstack-bootstrap" || cmd == "bookstack-check" || cmd == "bookstack-roles"
	if !known || err != nil || flags.NArg() != 0 || *check && cmd != "bookstack-roles" || *probe && cmd != "bookstack-directory" {
		fmt.Fprintln(diagnostics, bookstackUsage)
		return 2
	}
	api := stockbookstack.API{URL: strings.TrimSuffix(*base, "/"), TokenFile: *tokenFile}
	fail := func(what string, err error) int {
		fmt.Fprintf(diagnostics, "kyoffboard %s: %s: %v\n", cmd, what, err)
		return 1
	}
	switch cmd {
	case "bookstack-bootstrap":
		password, err := stockbookstack.ReadPassword(stdin)
		if err != nil {
			return fail("password", err)
		}
		defer clear(password)
		if err := stockbookstack.Bootstrap(ctx, api.URL, password, *tokenFile); err != nil {
			return fail("bootstrap", err)
		}
		fmt.Fprintln(out, "bootstrapped: one kyquickstart token, Editor for new accounts")
	case "bookstack-check":
		if err := stockbookstack.Check(ctx, api); err != nil {
			return fail("check", err)
		}
	case "bookstack-roles":
		if err := stockbookstack.MapRoles(ctx, api, *adminClaim, *userClaim, *check); err != nil {
			return fail("roles", err)
		}
	case "bookstack-directory":
		if *probe {
			return probeReceiver(ctx, *listen, diagnostics)
		}
		secret, err := os.ReadFile(*secretFile)
		if err != nil || len(secret) < 16 {
			return fail("directory secret", errors.New("unreadable or shorter than 16 bytes"))
		}
		rc := &stockbookstack.Receiver{API: api, Secret: secret, AdminClaim: *adminClaim, Log: out}
		srv := &http.Server{Addr: *listen, Handler: rc, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
		go func() {
			<-ctx.Done()
			shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			srv.Shutdown(shut)
		}()
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return fail("serve", errors.New("cannot listen"))
		}
	}
	return 0
}

// probeReceiver is the image's healthcheck: the receiver answers a GET with 405.
func probeReceiver(ctx context.Context, listen string, diagnostics io.Writer) int {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(diagnostics, "kyoffboard bookstack-directory: --listen is not host:port")
		return 2
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+stockbookstack.DirectoryPath, nil)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		fmt.Fprintln(diagnostics, "kyoffboard bookstack-directory: the receiver does not answer")
		return 1
	}
	res.Body.Close()
	return 0
}
