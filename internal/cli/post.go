package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
)

// Native mail setup proves configuration, while reporting external mail/recovery
// acceptance separately. It does not create identity grants or erase pending gates.
func postSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return nil, err
	}
	var caps struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != "kypost" || caps.BundleSchema != "kypost.setup/1" {
		return nil, errors.New("KyPost does not advertise the required setup contract")
	}
	for _, f := range []string{"sso", "retained-pairing-key", "domains", "recovery-pairing", "secret-files", "check"} {
		if !slices.Contains(caps.Features, f) {
			return nil, errors.New("KyPost lacks a required setup capability")
		}
	}
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	b, err = exec(ctx, args)
	if err != nil {
		return nil, err
	}
	var r struct {
		Version   int      `json:"version"`
		Applied   []string `json:"applied"`
		Unchanged []string `json:"unchanged"`
		DNS       []struct {
			Domain      string `json:"domain"`
			Name        string `json:"name"`
			Value       string `json:"value"`
			Established bool   `json:"established"`
		} `json:"dns"`
		Pending              []string `json:"pending"`
		ConfigurationApplied bool     `json:"configurationApplied"`
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &r) != nil || r.Version != 1 || !r.ConfigurationApplied || r.Applied == nil || r.Unchanged == nil || r.DNS == nil || r.Pending == nil || len(r.Applied)+len(r.Unchanged)+len(r.DNS)+len(r.Pending) > 128 || check && len(r.Applied) > 0 {
		return nil, errors.New("invalid or incomplete KyPost configuration report")
	}
	fields := append(slices.Clone(r.Applied), r.Unchanged...)
	fields = append(fields, r.Pending...)
	for _, d := range r.DNS {
		fields = append(fields, d.Domain, d.Name, d.Value)
	}
	for _, f := range fields {
		if !reportText(f, true) {
			return nil, errors.New("unsafe KyPost configuration report")
		}
	}
	return json.Marshal(r)
}
