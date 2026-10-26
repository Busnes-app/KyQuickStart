package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// KyDrive keeps its documents out of the sealed capsule: once it holds any, every backup first
// writes them to a Restic repository (KYDRIVE_BULK_BACKUP_REPOSITORY) and the capsule binds that
// snapshot. Without one, every backup fails with "KYDRIVE_BULK_BACKUP_REPOSITORY is required for
// document backups", KyRecovery deposits included. With a backup destination on KyDrive's
// target, KyDrive's bulk repository is <repository>/kydrive-bulk on that rest-server, beside
// kyquickstart's own <repository>/kydrive; the server's --private-repos keeps both under the
// target's user, and --append-only keeps the target from deleting either.
const (
	bulkRepository  = "kydrive-bulk"
	bulkPassword    = "bulk_backup_password"
	bulkCredentials = "/tmp/bulk-backup-credentials"
)

var composeSecretsLine = regexp.MustCompile(`(?m)^    secrets: \[([a-z_, ]+)\]\n`)

// bulkPlan returns KyDrive with its bulk repository on its target's backup destination, ok
// false without KyDrive or without a destination. It needs no connection, so review runs it too.
func bulkPlan(st *stack.Stack, cat catalog.Catalog) (drive catalog.App, ok bool, err error) {
	for _, a := range st.Apps {
		if a.Name != "kydrive" {
			continue
		}
		t, found := st.Target(a.Target)
		if !found || t.Backup == nil || t.Backup.CAFile != "" {
			return catalog.App{}, false, nil // with a private CA, see bulkUnavailable
		}
		drive, err = withBulkRepository(cat["kydrive"], *t.Backup)
		return drive, err == nil, err
	}
	return catalog.App{}, false, nil
}

// withBulkRepository points KyDrive at <repository>/kydrive-bulk. The target's rest-server
// password file is mounted read-only as a Compose secret, and KyDrive's entrypoint writes the
// two-line credentials file KyDrive reads (RESTIC_REST_USERNAME, RESTIC_REST_PASSWORD) into its
// private /tmp, mode 0600: the password is never in Compose, the environment or the state.
func withBulkRepository(drive catalog.App, dest stack.BackupDestination) (catalog.App, error) {
	if dest.CAFile != "" {
		return catalog.App{}, errors.New("kydrive's bulk repository is reached by KyDrive's own restic, which trusts only the system's certificate authorities; a backup destination with ca_file needs a certificate from a public CA (for example Let's Encrypt) before KyDrive can use it")
	}
	user := dest.RestUser()
	if user == "" || strings.ContainsAny(user, "'\"\\$`") {
		return catalog.App{}, fmt.Errorf("backup repository %s names no rest-server user", dest.Repository)
	}
	compose := string(drive.Compose)
	if len(composeSecretsLine.FindAllStringIndex(compose, -1)) != 1 {
		return catalog.App{}, errors.New("kydrive's Compose file no longer has exactly one service secrets list; its bulk repository cannot be connected")
	}
	m := composeSecretsLine.FindStringSubmatchIndex(compose)
	compose = compose[:m[3]] + ", " + bulkPassword + compose[m[3]:]
	for _, edit := range [][2]string{
		{"exec /app/kydrive-server\"]", "b=$$(cat /run/secrets/" + bulkPassword + ") || exit 1; (umask 077; printf 'RESTIC_REST_USERNAME=%s\\nRESTIC_REST_PASSWORD=%s\\n' '" + user + "' \\\"$$b\\\" > " + bulkCredentials + ") || exit 1; unset b; exec /app/kydrive-server\"]"},
		{"      KY_SCIM_TOKEN_FILE: /run/secrets/scim_token\n", "      KY_SCIM_TOKEN_FILE: /run/secrets/scim_token\n      KYDRIVE_BULK_BACKUP_REPOSITORY: \"" + strings.TrimRight(dest.Repository, "/") + "/" + bulkRepository + "\"\n      KYDRIVE_BULK_BACKUP_CREDENTIALS_FILE: " + bulkCredentials + "\n"},
		{"  scim_token:\n    file: ./secrets/scim_token\n", "  scim_token:\n    file: ./secrets/scim_token\n  " + bulkPassword + ":\n    file: " + dest.CredentialFile + "\n"},
	} {
		if strings.Count(compose, edit[0]) != 1 {
			return catalog.App{}, fmt.Errorf("kydrive's Compose file no longer has %q; its bulk repository cannot be connected", edit[0])
		}
		compose = strings.Replace(compose, edit[0], edit[1], 1)
	}
	drive.Compose = []byte(compose)
	return drive, nil
}

// bulkInitStep (kydrive.bulk-init) initializes KyDrive's bulk repository once, from inside its
// running container, where its entrypoint wrote the credentials. KyDrive's bulk-init changes
// nothing when the repository already opens with this deployment's key, and refuses one that
// does not, so every apply may run it.
type bulkInitStep struct {
	hash string
	run  func(ctx context.Context) error
}

func (s bulkInitStep) ID() string                            { return "kydrive.bulk-init" }
func (s bulkInitStep) InputHash() string                     { return s.hash }
func (s bulkInitStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s bulkInitStep) Apply(ctx context.Context) error       { return s.run(ctx) }
func (s bulkInitStep) Verify(context.Context) error          { return nil }

// bulkMissing is KyDrive's handover line when its target has no backup destination.
const bulkMissing = `  Document backups: KyDrive writes its files to a Restic repository before each backup, and
  its target has no backup destination to hold one. Every KyDrive backup and KyRecovery
  deposit fails once it holds a document ("KYDRIVE_BULK_BACKUP_REPOSITORY is required for
  document backups"). Give the target a backup destination with a certificate from a public CA
  (see "Backups and restore on Docker") and apply again.
`

// bulkUnavailable is KyDrive's handover line when its target's destination has its own CA:
// KyDrive's restic trusts only the system's, so its bulk repository stays unset there.
const bulkUnavailable = `  Document backups: KyDrive writes its files to a Restic repository before each backup, and
  this target's backup destination uses its own CA (ca_file), which KyDrive's restic does not
  trust. Until the destination has a certificate from a public CA, every KyDrive backup and
  KyRecovery deposit fails once it holds a document ("KYDRIVE_BULK_BACKUP_REPOSITORY is
  required for document backups"); kyquickstart backup kydrive still snapshots it cold.
`

// bulkInit is KyDrive's bulk-init step on t, nil unless t has a backup destination.
func bulkInit(d driver, t stack.Target) engine.Step {
	h, ok := d.(sshDriver)
	if !ok || t.Backup == nil || t.Backup.CAFile != "" {
		return nil
	}
	cmd := "cd " + remote.Quote(path.Join(t.Root, "kydrive")) + " && docker compose -p kyq-kydrive -f compose.yaml -f compose.kyq.yaml exec -T app /app/kydrive-server bulk-init"
	return bulkInitStep{
		hash: hex.EncodeToString(sha256Sum("kydrive.bulk-init\x00" + t.Backup.Repository)),
		run: func(ctx context.Context) error {
			if out, err := h.c.Run(ctx, cmd, nil); err != nil {
				return fmt.Errorf("kydrive bulk-init on %s: %w: %s", t.Name, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

func sha256Sum(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }
