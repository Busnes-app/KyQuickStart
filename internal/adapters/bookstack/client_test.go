package bookstack

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var testBinding = Binding{ID: 3, Issuer: "https://identity.example.test", Subject: "exact-subject"}

func report(b Binding, disabled, complete bool, tokens int64) []byte {
	data, _ := json.Marshal(map[string]any{
		"contract": contract, "module_version": moduleVersion, "user_id": b.ID,
		"binding_digest": digest(b), "disabled": disabled, "revocation_complete": complete, "tokens_remaining": tokens,
	})
	return data
}

func TestReportRejectsAmbiguousOrIncompleteAuthority(t *testing.T) {
	valid := report(testBinding, true, true, 0)
	var fields map[string]any
	json.Unmarshal(valid, &fields)
	for _, field := range []string{"disabled", "revocation_complete", "tokens_remaining", "binding_digest", "user_id", "module_version"} {
		t.Run(field, func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range fields {
				copy[k] = v
			}
			delete(copy, field)
			data, _ := json.Marshal(copy)
			if _, err := readReport(data, testBinding); err == nil {
				t.Fatal("missing authority accepted")
			}
		})
	}
	for _, data := range [][]byte{
		report(Binding{ID: 4, Issuer: testBinding.Issuer, Subject: testBinding.Subject}, true, true, 0),
		report(Binding{ID: 3, Issuer: testBinding.Issuer, Subject: "another-subject"}, true, true, 0),
		report(testBinding, false, true, 0), report(testBinding, true, true, -1),
		append(append([]byte{}, valid...), []byte("\n{}")...), []byte(strings.Repeat("x", 16385)),
	} {
		if _, err := readReport(data, testBinding); err == nil {
			t.Fatal("invalid authority accepted")
		}
	}
}

func TestPartialPublicationResumesEvenWithNoTokens(t *testing.T) {
	var actions []string
	c, err := New(func(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
		if len(args) != 3 || args[0] != "sh" || args[1] != "-c" || !strings.Contains(args[2], "mktemp -d") {
			t.Fatal("native invocation did not isolate input")
		}
		var request struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(stdin, &request) != nil {
			t.Fatal("invalid request")
		}
		actions = append(actions, request.Action)
		if len(actions) == 1 {
			return report(testBinding, true, false, 0), nil
		}
		return report(testBinding, true, true, 0), nil
	}, "/config", "/app/www/artisan")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Deprovision(context.Background(), testBinding); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"check", "deprovision", "check"}) {
		t.Fatal("partial publication skipped repair or verification")
	}
}

func TestInvalidBindingDoesNotExecuteAndErrorsDoNotLeak(t *testing.T) {
	calls := 0
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		return []byte("secret-native-body"), errors.New("secret-native-stderr")
	}, "/config", "/app/www/artisan")
	for _, b := range []Binding{
		{ID: 0, Issuer: testBinding.Issuer, Subject: testBinding.Subject},
		{ID: 3, Issuer: "http://identity.example.test", Subject: testBinding.Subject},
		{ID: 3, Issuer: testBinding.Issuer + "?", Subject: testBinding.Subject},
		{ID: 3, Issuer: testBinding.Issuer, Subject: "bad\nsubject"},
	} {
		if _, err := c.Inspect(context.Background(), b); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input crossed the target boundary")
	}
	if _, err := c.Inspect(context.Background(), testBinding); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("target error was not sanitized")
	}
}

func TestUnsupportedModuleCannotMutate(t *testing.T) {
	var actions []string
	c, _ := New(func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		var request struct {
			Action string `json:"action"`
		}
		json.Unmarshal(input, &request)
		actions = append(actions, request.Action)
		return []byte(strings.ReplaceAll(string(report(testBinding, false, false, 1)), moduleVersion, "0.1.0")), nil
	}, "/config", "/app/www/artisan")
	if err := c.Deprovision(context.Background(), testBinding); err == nil || !reflect.DeepEqual(actions, []string{"check"}) {
		t.Fatal("unsupported module reached mutation")
	}
}
