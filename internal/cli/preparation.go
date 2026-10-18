package cli

import (
	"context"
	"encoding/json"
	"errors"
)

// stageIdentity stages intent: typed preparation settings, or an amendment bundle.
func stageIdentity(ctx context.Context, exec identityExec, inputExec func(context.Context, []string, []byte) ([]byte, error), intent any, bundle string, check bool, extra ...string) error {
	if err := identityCapabilities(ctx, exec, true, extra...); err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil || len(raw) > 128<<10 {
		return errors.New("KyIdentity preparation exceeds native input limit")
	}
	return stageNativeBundle(ctx, inputExec, raw, bundle, "kyidentity.setup-preparation/1", check)
}

// Both native preparation adapters require present-only readback before setup.
func stageNativeBundle(ctx context.Context, inputExec func(context.Context, []string, []byte) ([]byte, error), raw []byte, bundle, schema string, check bool) error {
	return nativeInputReceipt(ctx, inputExec, "--stage", raw, bundle, schema, check)
}

func nativeInputReceipt(ctx context.Context, inputExec func(context.Context, []string, []byte) ([]byte, error), mode string, raw []byte, bundle, schema string, check bool) error {
	args := []string{"apply-setup", mode, "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	output, err := inputExec(ctx, args, raw)
	if err != nil {
		return err
	}
	var receipt struct {
		Schema string `json:"schema"`
		Status string `json:"status"`
	}
	if len(output) > 1024 || json.Unmarshal(output, &receipt) != nil || receipt.Schema != schema || receipt.Status != "present" && (check || receipt.Status != "created") {
		return errors.New("native bundle preparation was not verified")
	}
	if !check {
		return nativeInputReceipt(ctx, inputExec, mode, raw, bundle, schema, true)
	}
	return nil
}
