package cli

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
)

var immichNativeID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var immichSubject = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)

type immichSetupAccount struct {
	ID            string `json:"id"`
	Subject       string `json:"subject"`
	Administrator *bool  `json:"administrator"`
}
type immichSetupReport struct {
	Schema   string               `json:"schema"`
	SetupID  string               `json:"setupId"`
	Check    *bool                `json:"check"`
	Created  []string             `json:"created"`
	Planned  []string             `json:"planned"`
	Accounts []immichSetupAccount `json:"accounts"`
}

func readImmichSetupReport(raw []byte, check bool) (immichSetupReport, error) {
	var r immichSetupReport
	if len(raw) > 128<<10 || json.Unmarshal(raw, &r) != nil || r.Schema != "immich.setup-report/1" ||
		!productSetupID.MatchString(r.SetupID) || r.Check == nil || *r.Check != check || r.Created == nil || r.Planned == nil ||
		len(r.Planned) != 0 || len(r.Accounts) != 2 || (check && len(r.Created) != 0) ||
		(len(r.Created) != 0 && !slices.Equal(r.Created, []string{"provider", "administrator", "everyday"})) {
		return r, errors.New("Immich setup is incomplete or its report is invalid")
	}
	for i, a := range r.Accounts {
		if !immichNativeID.MatchString(a.ID) || !immichSubject.MatchString(a.Subject) || a.Administrator == nil || *a.Administrator != (i == 0) {
			return r, errors.New("Immich setup lacks exact native account and administrator proofs")
		}
	}
	if r.Accounts[0].ID == r.Accounts[1].ID || r.Accounts[0].Subject == r.Accounts[1].Subject {
		return r, errors.New("Immich administrator and everyday accounts must be distinct")
	}
	return r, nil
}

// This is explicit initial setup of the community module, not catalog admission,
// an ongoing directory connector or proof of administrator workflow separation.
func immichSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	raw, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return nil, err
	}
	var caps struct {
		Schema, Service, BundleSchema, NativeVersion, ModuleContract string
		Features                                                     []string
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" ||
		caps.Service != "immich" || caps.BundleSchema != "immich.setup/1" || caps.NativeVersion != "3.2.0" || caps.ModuleContract != "ky.immich-setup/1" {
		return nil, errors.New("Immich does not advertise the qualified native setup module")
	}
	for _, feature := range []string{"native-oidc", "owned-initial-accounts", "immutable-receipts", "check"} {
		if !slices.Contains(caps.Features, feature) {
			return nil, errors.New("Immich native setup capability is missing")
		}
	}
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	raw, err = exec(ctx, args)
	if err != nil {
		return nil, err
	}
	r, err := readImmichSetupReport(raw, check)
	if err != nil {
		return nil, err
	}
	if !check {
		raw, err = exec(ctx, append(args, "--check"))
		if err != nil {
			return nil, err
		}
		verified, err := readImmichSetupReport(raw, true)
		if err != nil {
			return nil, err
		}
		if verified.SetupID != r.SetupID {
			return nil, errors.New("Immich installation ownership changed during verification")
		}
		for i, account := range verified.Accounts {
			if account.ID != r.Accounts[i].ID || account.Subject != r.Accounts[i].Subject || *account.Administrator != *r.Accounts[i].Administrator {
				return nil, errors.New("Immich native account ownership changed during verification")
			}
		}
	}
	// Emit fixed names and native UUIDs only; arbitrary product fields and OIDC
	// subject text remain inside validation and never enter workstation output.
	objects := []productObject{{Kind: "configuration", Name: "native-oidc"},
		{Kind: "identity", Name: "owner-administrator", ID: r.Accounts[0].ID},
		{Kind: "identity", Name: "everyday", ID: r.Accounts[1].ID}}
	out := productReport{Schema: "ky.product-setup-report/1", Service: "immich", SetupID: r.SetupID,
		Created: []productObject{}, Present: []productObject{}, Pending: []productObject{}}
	if len(r.Created) == 0 {
		out.Present = objects
	} else {
		out.Created = objects
	}
	return json.Marshal(out)
}
