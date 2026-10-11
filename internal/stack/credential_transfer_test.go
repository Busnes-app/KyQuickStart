package stack

import (
	"strings"
	"testing"
)

func credentialTransferPlan() string {
	return strings.Replace(identityPreparationPlan, "integrations:", "- product: kynotes\n  target: nas\n  url: https://notes.example.test\nintegrations:", 1) + `- product: kynotes
  target: nas
  container: notes
  bundle: /data/installer/setup.json
  identity_client: notes
  notes:
    version: 1
    sso:
      issuer_url: https://identity.example.test
      client_id: notes
      redirect_uri: https://notes.example.test/api/v1/auth/oidc/callback
      client_secret_file: /data/installer/client.secret
      directory_hmac_secret_file: /data/installer/directory.secret
`
}

func TestCredentialTransferGraphBoundary(t *testing.T) {
	plan := credentialTransferPlan()
	s, err := Parse([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	source, client, err := s.IdentitySource(s.Integrations[1])
	if err != nil || source.Product != "kyidentity" || client.ID != "notes" {
		t.Fatal("source selection failed", err)
	}
	for _, change := range [][2]string{
		{"identity_client: notes", "identity_client: other"},
		{"administrator_role: kynotes.admin", "administrator_role: admin"},
		{"everyday_role: kynotes.user", "everyday_role: user"},
		{"client_secret_file: /data/installer/client.secret", "client_secret_file: /data/foreign.secret"},
		{"directory_hmac_secret_file: /data/installer/directory.secret", "directory_hmac_secret_file: /data/foreign.secret"},
		{"url: https://notes.example.test/api/v1/sync/events", "url: https://other.example.test/api/v1/sync/events"},
		{"redirect_uri: https://notes.example.test/api/v1/auth/oidc/callback", "redirect_uri: https://other.example.test/api/v1/auth/oidc/callback"},
		{"url: https://identity.example.test", "url: https://other.example.test"},
		{"    version: 1\n    sso:", "    version: 1\n    admins:\n    - issuer: https://identity.example.test\n      subject: guessed\n      username: owner-admin\n    sso:"},
		{"    version: 1\n    sso:", "    version: 1\n    client_secret: forbidden\n    sso:"},
	} {
		if _, err := Parse([]byte(strings.Replace(plan, change[0], change[1], 1))); err == nil {
			t.Fatal("invalid transfer graph accepted", change[0])
		}
	}
}
