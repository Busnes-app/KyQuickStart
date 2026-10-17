package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestMarkerRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := realTemp(t)
	r := remote.Local{}
	if m, err := ReadMarker(ctx, r, root); err != nil || m != (stack.Marker{}) {
		t.Fatalf("absent marker = %+v, %v", m, err)
	}
	want := stack.Marker{Revision: 7, Owner: "runner", RunnerID: "0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c", InstallationID: "271333e2-c221-4601-b249-563b3d828530"}
	if err := WriteMarker(ctx, r, root, want); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMarker(ctx, r, root); err != nil || m != want {
		t.Fatalf("marker = %+v, %v", m, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, ".plan")); fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode %v", fi.Mode())
	}
	for name, body := range map[string]string{
		"unknown field": `{"revision":1,"owner":"cli","runner_id":"","extra":1}`,
		"bad owner":     `{"revision":1,"owner":"kyyard","runner_id":""}`,
		"negative":      `{"revision":-1,"owner":"cli","runner_id":""}`,
		"not json":      `revision: 1`,
		"bad id":        `{"revision":1,"owner":"cli","runner_id":"","installation_id":"../x"}`,
	} {
		os.WriteFile(filepath.Join(root, ".plan"), []byte(body), 0o600)
		if _, err := ReadMarker(ctx, r, root); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
