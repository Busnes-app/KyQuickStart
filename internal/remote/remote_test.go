package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestQuoteSurvivesShell(t *testing.T) {
	for _, s := range []string{"", "plain", "a b", "it's", "$(id)", "`id`", "a\nb", `\'"`} {
		out, err := Local{}.Run(context.Background(), "printf %s "+Quote(s), nil)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("Quote(%q) came back as %q", s, out)
		}
	}
}

func TestLocalExitError(t *testing.T) {
	out, err := Local{}.Run(context.Background(), "echo out; echo err >&2; exit 3", nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want exit 3", err)
	}
	if string(out) != "out\n" || string(ee.Stderr) != "err\n" {
		t.Errorf("stdout %q, stderr %q", out, ee.Stderr)
	}
	if !strings.Contains(ee.Error(), "exit status 3: err") {
		t.Errorf("Error() = %q", ee.Error())
	}
}

func TestLocalStdin(t *testing.T) {
	out, err := Local{}.Run(context.Background(), "cat", []byte("hello"))
	if err != nil || string(out) != "hello" {
		t.Fatalf("out %q, err %v", out, err)
	}
}
