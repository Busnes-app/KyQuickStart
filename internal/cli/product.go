package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

type productAdapter struct {
	binary      string
	runtimeUser string
	setup       func(context.Context, identityExec, string, bool) ([]byte, error)
}

// Entries are enabled only after the product-owned interface is implemented.
var productAdapters = map[string]productAdapter{
	"kyidentity": {identityBinary, "", identitySetup},
	"kydrive":    {"/app/kydrive-server", "", driveSetup},
	"kynotes":    {"/kynotes-server", "", notesSetup},
	"kypulse":    {"/app/kypulse", "", pulseSetup},
	"kypost":     {"/usr/local/bin/kypost-server", "kypost", postSetup},
	"kydns":      {"/usr/local/bin/kydns", "", dnsSetup},
	"kyrecovery": {"/app/kyrecovery", "kyrecovery", recoverySetup},
	"kymessage":  {"/app/kymessages", "", messageSetup},
	"kycalendar": {"/app/kycalendar", "", calendarSetup},
	"kyyard":     {"/app/kyyard-server", "", yardSetup},
	"immich":     {"/usr/src/app/server/bin/immich-admin", "", immichSetup},
}

func validateProductTarget(product string, target stack.Target) error {
	if product == "immich" && target.Kubernetes() {
		return errors.New("Immich integration requires a Docker target")
	}
	// Kubernetes exec cannot switch users, so a required runtime user needs Docker.
	if productAdapters[product].runtimeUser != "" && target.Kubernetes() {
		return errors.New("this product's setup must run as its service user, which requires a Docker target")
	}
	return nil
}

type productObject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}
type productReport struct {
	Schema  string          `json:"schema"`
	Service string          `json:"service"`
	SetupID string          `json:"setupId"`
	Created []productObject `json:"created"`
	Present []productObject `json:"present"`
	Pending []productObject `json:"pending"`
}

var productSetupID = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func reportText(s string, required bool) bool {
	return (!required || s != "") && len(s) <= 256 && strings.IndexFunc(s, unicode.IsControl) < 0
}

func readProductReport(b []byte, service string, check bool) (productReport, error) {
	var r productReport
	if len(b) > 128<<10 || json.Unmarshal(b, &r) != nil || r.Schema != "ky.product-setup-report/1" || r.Service != service || !productSetupID.MatchString(r.SetupID) || r.Created == nil || r.Present == nil || r.Pending == nil {
		return r, errors.New("invalid product setup report")
	}
	if check && len(r.Created) != 0 {
		return r, errors.New("product inspection reported mutations")
	}
	if len(r.Created)+len(r.Present)+len(r.Pending) > 128 {
		return r, errors.New("product setup report exceeds object limit")
	}
	for _, items := range [][]productObject{r.Created, r.Present, r.Pending} {
		for _, obj := range items {
			if (obj.Kind != "identity" && obj.Kind != "configuration" && obj.Kind != "activation" && obj.Kind != "backup") || !reportText(obj.Name, true) || !reportText(obj.ID, false) {
				return r, errors.New("invalid product object report")
			}
		}
	}
	return r, nil
}

func driveSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kydrive", []string{"kyidentity-oidc", "scim", "dedicated-admin", "check", "restart"}, bundle, check)
}

func commonProductSetup(ctx context.Context, exec identityExec, service string, features []string, bundle string, check bool) ([]byte, error) {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return nil, err
	}
	var caps struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != service || caps.BundleSchema != "ky.product-setup/1" {
		return nil, errors.New("product does not advertise the required setup contract")
	}
	for _, required := range features {
		found := false
		for _, f := range caps.Features {
			found = found || f == required
		}
		if !found {
			return nil, fmt.Errorf("product lacks setup capability %s", required)
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
	r, err := readProductReport(b, service, check)
	if err != nil {
		return nil, err
	}
	if len(r.Pending) == 0 {
		return json.Marshal(r)
	}
	if check {
		return nil, errors.New("product configuration is pending activation")
	}
	for _, obj := range r.Pending {
		if obj.Kind != "activation" || obj.Name != "restart" {
			return nil, errors.New("product reports unfinished setup requirements")
		}
	}
	if _, err = exec(ctx, []string{"apply-setup", "--restart"}); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return nil, errors.New("product activation not verified; rerun read-only inspection after restart")
		case <-ticker.C:
			b, err = exec(waitCtx, []string{"apply-setup", "--file", bundle, "--check"})
			if err != nil {
				continue
			}
			verified, err := readProductReport(b, service, true)
			if err != nil {
				return nil, err
			}
			if verified.SetupID != r.SetupID {
				return nil, errors.New("product setup ownership changed during activation")
			}
			if len(verified.Pending) == 0 {
				r.Pending = []productObject{}
				return json.Marshal(r)
			}
		}
	}
}

func pulseSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kypulse", []string{"kyidentity-oidc", "directory-webhook", "dedicated-admin", "monitoring-targets", "log-sources", "offboarding-alerts", "check", "restart"}, bundle, check)
}

func dnsSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kydns", []string{"kyidentity-oidc", "exact-admin-subjects", "admin-only", "directory-webhook", "check"}, bundle, check)
}

func recoverySetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kyrecovery", []string{"kyidentity-oidc", "exact-admin-subjects", "admin-only", "directory-webhook", "check"}, bundle, check)
}

func messageSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kymessage", []string{"kyidentity-oidc", "directory-webhook", "dedicated-admin", "check", "restart"}, bundle, check)
}

func calendarSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kycalendar", []string{"kyidentity-oidc", "directory-webhook", "dedicated-admin", "check", "restart"}, bundle, check)
}

func yardSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	return commonProductSetup(ctx, exec, "kyyard", []string{"kyidentity-oidc", "directory-webhook", "dedicated-admin", "check"}, bundle, check)
}
