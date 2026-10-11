package stack

import (
	"strings"
	"testing"
)

const notesPreparationPlan = `version: 1
targets:
- name: nas
  host: nas.example.test
  user: kyq
existing:
- product: kynotes
  target: nas
  url: https://notes.example.test
integrations:
- product: kynotes
  target: nas
  container: notes
  bundle: /data/installer/setup.json
  notes:
    version: 1
    sso:
      issuer_url: https://identity.example.test
      client_id: notes
      client_secret_file: /data/installer/client.secret
      redirect_uri: https://notes.example.test/api/v1/auth/oidc/callback
    admins:
    - issuer: https://identity.example.test
      subject: dedicated-owner-subject
      username: owner-admin
    backup:
      dir: /backups
      keep: 7
      deposit_interval: 24h
`

func TestNotesPreparationBoundary(t *testing.T) {
	if _, err := Parse([]byte(notesPreparationPlan)); err != nil {
		t.Fatal("valid preparation refused", err)
	}
	for _, bad := range []string{
		strings.ReplaceAll(notesPreparationPlan, "kynotes", "kypulse"),
		strings.Replace(notesPreparationPlan, "notes:\n    version: 1", "notes:\n    version: 2", 1),
		strings.Replace(notesPreparationPlan, "client_secret_file:", "client_secret:", 1),
		strings.Replace(notesPreparationPlan, "client_secret_file: /data/installer/client.secret", "client_secret_file: /data/installer/setup.json", 1),
		strings.Replace(notesPreparationPlan, "https://identity.example.test", "https://user:secret@identity.example.test", 1),
		strings.Replace(notesPreparationPlan, "deposit_interval: 24h", "deposit_interval: 1m", 1),
		strings.Replace(notesPreparationPlan, "deposit_interval: 24h", "deposit_interval: 8785h", 1),
		strings.Replace(notesPreparationPlan, "redirect_uri: https://notes.example.test/api/v1/auth/oidc/callback", "redirect_uri: https://notes.example.test/wrong", 1),
		strings.Replace(notesPreparationPlan, "bundle: /data/installer/setup.json", "bundle: /data/foreign.json", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("unsafe preparation accepted")
		}
	}
}
