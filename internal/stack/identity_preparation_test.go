package stack

import (
	"strings"
	"testing"
)

const identityPreparationPlan = `version: 1
targets:
- name: nas
  host: nas.example.test
  user: kyq
existing:
- product: kyidentity
  target: nas
  url: https://identity.example.test
integrations:
- product: kyidentity
  target: nas
  container: identity
  bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json
  identity:
    schema: kyidentity.setup/1
    setup_id: 271333e2-c221-4601-b249-563b3d828530
    admin:
      username: owner-admin
      email: admin@example.test
    everyday:
      username: owner
      email: owner@example.test
    clients:
    - id: notes
      name: KyNotes
      redirect_uris: [https://notes.example.test/api/v1/auth/oidc/callback]
      administrator_role: kynotes.admin
      everyday_role: kynotes.user
      suite_webhook:
        url: https://notes.example.test/api/v1/sync/events
`

func TestIdentityPreparationBoundary(t *testing.T) {
	if _, err := Parse([]byte(identityPreparationPlan)); err != nil {
		t.Fatal("valid preparation refused", err)
	}
	for _, bad := range []string{
		strings.Replace(identityPreparationPlan, "bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json", `bundle: "/data/\u001b[31m/installer/271333e2-c221-4601-b249-563b3d828530/setup.json"`, 1),
		strings.Replace(identityPreparationPlan, "product: kyidentity", "product: kynotes", -1),
		strings.Replace(identityPreparationPlan, "schema: kyidentity.setup/1", "schema: kyidentity.setup/2", 1),
		strings.Replace(identityPreparationPlan, "username: owner\n", "username: owner-admin\n", 1),
		strings.Replace(identityPreparationPlan, "email: owner@example.test", "email: admin@example.test", 1),
		strings.Replace(identityPreparationPlan, "username: owner-admin", "username: owner-admin\n      password: forbidden", 1),
		strings.Replace(identityPreparationPlan, "username: owner-admin", "username: owner-admin\n      password_file: /outside/credential", 1),
		strings.Replace(identityPreparationPlan, "name: KyNotes", "name: KyNotes\n      secret_file: /outside/credential", 1),
		strings.Replace(identityPreparationPlan, "everyday_role: kynotes.user", "everyday_role: KYNOTES.ADMIN", 1),
		strings.Replace(identityPreparationPlan, "bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json", "bundle: /data/foreign/setup.json", 1),
		strings.Replace(identityPreparationPlan, "https://notes.example.test/api/v1/sync/events", "https://notes.example.test/api/v1/sync/events?token=secret", 1),
		strings.Replace(identityPreparationPlan, "suite_webhook:\n        url: https://notes.example.test/api/v1/sync/events", "scim:\n        url: https://bridge.example.test/scim/v2/notes\n        token_file: /data/installer/271333e2-c221-4601-b249-563b3d828530/admin.password", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("unsafe Identity preparation accepted")
		}
	}
}
