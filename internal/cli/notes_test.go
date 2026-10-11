package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestNotesAdapterChecksNativeContractAndDropsDetails(t *testing.T) {
	caps := `{"schema":"ky.setup-capabilities/1","service":"kynotes","bundleSchema":"kynotes.setup/1","features":["sso","issuer-bound-admin","dedicated-admin","protected-setup-files","backup","check"]}`
	for _, status := range []string{"present", "created", "conflict"} {
		calls := 0
		out, err := notesSetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(caps), nil
			}
			if !strings.HasSuffix(strings.Join(args, " "), "--check") {
				t.Fatal("inspection applied bundle")
			}
			return []byte(`{"version":1,"results":[{"section":"sso","status":"` + status + `","detail":"credential-never-print"}],"handover":{}}`), nil
		}, "/data/bundle.json", true)
		if status == "present" && (err != nil || strings.Contains(string(out), "credential-never-print")) || status != "present" && err == nil {
			t.Fatal(status, string(out), err)
		}
	}
}

func TestNotesPreparationCapabilityAndReadbackBeforeApply(t *testing.T) {
	n := &stack.NotesPreparation{Version: 1, Backup: &stack.NotesBackup{Keep: 7}}
	for _, mode := range []string{"supported", "unsupported", "mutating-check", "invalid-receipt"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			exec := func(context.Context, []string) ([]byte, error) {
				caps := `{"schema":"ky.setup-capabilities/1","service":"kynotes","bundleSchema":"kynotes.setup/1","features":["sso","issuer-bound-admin","dedicated-admin","protected-setup-files","bundle-preparation","backup","check"]}`
				if mode == "unsupported" {
					caps = strings.Replace(caps, `"bundle-preparation",`, "", 1)
				}
				return []byte(caps), nil
			}
			err := stageNotes(context.Background(), exec, func(_ context.Context, args []string, input []byte) ([]byte, error) {
				calls++
				if !strings.Contains(string(input), `"keep":7`) || strings.Contains(strings.Join(args, " "), `"keep":7`) {
					t.Fatal("metadata missing from stdin or included in argv")
				}
				status := "created"
				if calls == 2 && mode != "mutating-check" {
					if args[len(args)-1] != "--check" {
						t.Fatal("readback mutated preparation")
					}
					status = "present"
				}
				if mode == "invalid-receipt" {
					status = "conflict"
				}
				return []byte(`{"schema":"kynotes.setup-preparation/1","status":"` + status + `","detail":"never-print"}`), nil
			}, n, "/data/installer/setup.json", false)
			if mode == "supported" && (err != nil || calls != 2) || mode != "supported" && err == nil || mode == "unsupported" && calls != 0 {
				t.Fatal(mode, calls, err)
			}
		})
	}
}

func TestNotesAdapterRefusesUnsafeBuildBeforeMutation(t *testing.T) {
	for _, missing := range []string{"dedicated-admin", "protected-setup-files"} {
		t.Run(missing, func(t *testing.T) {
			calls := 0
			_, err := notesSetup(context.Background(), func(context.Context, []string) ([]byte, error) {
				calls++
				caps := `{"schema":"ky.setup-capabilities/1","service":"kynotes","bundleSchema":"kynotes.setup/1","features":["sso","issuer-bound-admin","dedicated-admin","protected-setup-files","backup","check"]}`
				return []byte(strings.Replace(caps, `"`+missing+`",`, "", 1)), nil
			}, "/data/bundle.json", false)
			if err == nil || calls != 1 {
				t.Fatal("unsafe build reached setup", err, calls)
			}
		})
	}
}
