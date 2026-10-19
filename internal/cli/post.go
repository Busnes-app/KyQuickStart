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

// postBundle is the catalog KyPost's native version-1 setup: sign-in through KyIdentity, the
// client secret in a file beside the bundle. autoProvision creates an account at first sign-in
// for whoever KyIdentity lets through; KyIdentity's assignment to the client is the gate.
func postBundle(f clientCredentials, secretFile string) any {
	type sso struct {
		Enabled       bool   `json:"enabled"`
		IssuerURL     string `json:"issuerUrl"`
		ClientID      string `json:"clientId"`
		AutoProvision bool   `json:"autoProvision"`
	}
	return struct {
		Version          int    `json:"version"`
		SSO              sso    `json:"sso"`
		ClientSecretFile string `json:"clientSecretFile"`
	}{1, sso{true, f.IssuerURL, f.ClientID, true}, secretFile}
}

// postContainer is the catalog KyPost's container: Compose project kyq-kypost, service app.
const postContainer = "kyq-kypost-app-1"

// postHandover is printed after every apply that installs KyPost; %[4]s is its container,
// %[5]s its Docker target.
const postHandover = `
KyPost at https://%[1]s/: webmail for mail accounts you already have. Sign in through
  KyIdentity as %[2]s, for administration only (in this release it can also add mail
  accounts, so keep mail to the other account), or as %[3]s for mail. Anyone else needs
  KyIdentity role kypost.user and gets a KyPost account at first sign-in.
  KyPost reads and sends through existing IMAP/SMTP accounts and opens no mail port. Native
  mail, receiving, a relay and domains are not set up. KyPost refuses mail servers on private
  (LAN) addresses, and this release offers no setting to allow one: use providers reachable at
  a public address.
  On its first start KyPost downloads its Ollama model (about 2.9 GB) and retries until it has
  it; until then a lighter built-in sorter labels mail, and KyPost reports healthy.
  Outstanding human steps:
  1. Its local "admin" break-glass account: separate from KyIdentity's %[2]s, for when
     KyIdentity is down; you never need it otherwise. Read its one-time password, on %[5]s:
       docker exec --user kypost %[4]s cat /kypost/config/first-run-password.txt
     Sign in with it once at https://%[1]s/ with KyPost's own password sign-in, not KyIdentity,
     and change it. Only after that, delete the file:
       docker exec --user kypost %[4]s rm /kypost/config/first-run-password.txt
  2. Offboarding: removing someone in KyIdentity stops new sign-ins, but KyPost keeps a live
     session until it restarts. To end every live session, on %[5]s:
       docker restart %[4]s
  3. Pair it with KyRecovery once KyRecovery's custodian ceremony is done: it needs a fresh
     one-time code from KyRecovery's administrator. It makes no recovery backups until then.
`
