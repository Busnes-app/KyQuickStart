package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandBoundaryDoesNotReflectInputs(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {nil, 2}, {[]string{"--unknown=PRIVATE-CANARY"}, 2},
		{[]string{"--config", "/missing/PRIVATE-CANARY.json", "--check"}, 1},
		{[]string{"--config", "/missing/config.json", "PRIVATE-CANARY"}, 2},
		{[]string{"--config", "/missing/PRIVATE-CANARY.json", "--init-tls"}, 1},
		{[]string{"--config", "/missing/PRIVATE-CANARY.json", "--healthcheck"}, 1},
		{[]string{"--config", "/missing/config.json", "--check", "--init-tls"}, 2},
		{[]string{"--config", "/missing/config.json", "--check", "--healthcheck"}, 2},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), test.args, &out, &diagnostics); code != test.code {
			t.Fatal("wrong exit", code)
		}
		if strings.Contains(out.String()+diagnostics.String(), "PRIVATE-CANARY") {
			t.Fatal("private input reflected")
		}
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory", err)
	}
	return dir
}

func TestInitTLSThenServeAndHealthcheck(t *testing.T) {
	directory, state := privateDir(t), privateDir(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := map[string]any{"schema": "ky.offboarding-runtime/1", "setup_id": "271333e2-c221-4601-b249-563b3d828530", "listen": address, "state_dir": state,
		"cert_file": "server.pem", "key_file": "server-key.pem", "apps": []any{map[string]any{"app": "wiki", "kind": "bookstack", "origin": "https://wiki.example.test",
			"issuer": "https://identity.example.test", "product_token_file": "product-token", "scim_token_file": "scim-token"}}}
	raw, _ := json.Marshal(cfg)
	file := filepath.Join(directory, "config.json")
	for name, data := range map[string][]byte{"config.json": raw, "product-token": []byte(strings.Repeat("ab", 32)), "scim-token": []byte(strings.Repeat("cd", 32))} {
		if os.WriteFile(filepath.Join(directory, name), data, 0600) != nil {
			t.Fatal("fixture staging failed")
		}
	}
	var out, diagnostics bytes.Buffer
	for range 2 {
		if code := run(context.Background(), []string{"--config", file, "--init-tls"}, &out, &diagnostics); code != 0 || out.Len()+diagnostics.Len() != 0 {
			t.Fatal("init-tls failed or printed", code)
		}
	}
	if run(context.Background(), []string{"--config", file, "--healthcheck"}, &out, &diagnostics) != 1 || diagnostics.String() != "kyoffboard: listener unhealthy\n" || out.Len() != 0 {
		t.Fatal("healthcheck passed without a listener")
	}
	diagnostics.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--config", file}, &out, &diagnostics) }()
	deadline := time.Now().Add(5 * time.Second)
	for run(context.Background(), []string{"--config", file, "--healthcheck"}, &out, &bytes.Buffer{}) != 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("served bridge never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 || out.Len()+diagnostics.Len() != 0 {
		t.Fatal("bridge did not stop cleanly", code)
	}
}
