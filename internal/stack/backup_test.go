package stack

import (
	"strings"
	"testing"
)

const backupBlock = "    root: /srv/kyq\n    backup:\n      repository: rest:https://backup.lan:8000/nas\n      credential_file: /home/kyq/.config/kyq/rest-password\n      ca_file: /home/kyq/.config/kyq/rest-ca.pem\n"

func TestBackupDestination(t *testing.T) {
	s, err := Parse([]byte(strings.Replace(good, "    root: /srv/kyq\n", backupBlock, 1)))
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := s.Target("k1")
	if b := k1.Backup; b == nil || b.RestUser() != "nas" || b.CAFile == "" {
		t.Fatalf("backup = %+v", k1.Backup)
	}
}

// Work 2's checks: every fault is refused by Parse, before any connection.
func TestBackupDestinationRejects(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"http":          {"rest:https://", "rest:http://", "rest:https"},
		"no rest":       {"rest:https://", "https://", "rest:https"},
		"sftp":          {"rest:https://backup.lan:8000/nas", "sftp:backup.lan:/nas", "rest:https"},
		"userinfo":      {"https://backup", "https://nas:pw@backup", "no credentials"},
		"query":         {"8000/nas", "8000/nas?x=1", "no credentials"},
		"fragment":      {"8000/nas", "8000/nas#x", "no credentials"},
		"no user":       {"8000/nas", "8000/", "one path segment"},
		"two segments":  {"8000/nas", "8000/nas/x", "one path segment"},
		"bad port":      {":8000", ":99999", "port"},
		"bad host":      {"backup.lan", "back_up.lan", "hostname"},
		"relative cred": {"credential_file: /home", "credential_file: home", "credential_file"},
		"dotdot ca":     {"ca_file: /home/kyq", "ca_file: /home/../kyq", "ca_file"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(strings.Replace(good, "    root: /srv/kyq\n", backupBlock, 1), c.old, c.new, 1)
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	kube := strings.Replace(kubeStack, "context: admin@k1", "context: admin@k1\n    backup:\n      repository: rest:https://b.lan/k1\n      credential_file: /a", 1)
	if _, err := Parse([]byte(kube)); err == nil || !strings.Contains(err.Error(), "for Docker targets") {
		t.Fatalf("kubernetes backup: err = %v", err)
	}
}
