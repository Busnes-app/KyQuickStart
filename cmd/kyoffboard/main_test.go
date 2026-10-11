package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCommandBoundaryDoesNotReflectInputs(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {nil, 2}, {[]string{"--unknown=PRIVATE-CANARY"}, 2},
		{[]string{"--config", "/missing/PRIVATE-CANARY.json", "--check"}, 1},
		{[]string{"--config", "/missing/config.json", "PRIVATE-CANARY"}, 2},
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
