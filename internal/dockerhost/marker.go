package dockerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const maxMarker = 4096

// ReadMarker returns <root>/.plan, or the zero Marker when there is none.
func ReadMarker(ctx context.Context, r remote.Runner, root string) (stack.Marker, error) {
	f := remote.Quote(path.Join(root, ".plan"))
	out, err := r.Run(ctx, fmt.Sprintf("test ! -e %s || head -c %d %s", f, maxMarker+1, f), nil)
	if err != nil {
		return stack.Marker{}, fmt.Errorf("read plan marker: %w", err)
	}
	if len(out) == 0 {
		return stack.Marker{}, nil
	}
	var m stack.Marker
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if len(out) > maxMarker || dec.Decode(&m) != nil {
		return stack.Marker{}, errors.New("plan marker is not valid JSON")
	}
	return m, m.Validate()
}

func WriteMarker(ctx context.Context, r remote.Runner, root string, m stack.Marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return WriteConfig(ctx, r, path.Join(root, ".plan"), b)
}
