//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"maunium.net/go/mautrix/id"
)

// prepareMessageUpgrade selects an independently attested historical manifest,
// keeping the existing fixture's transport and CA plumbing. The returned check
// upgrades only after ordinary users have exchanged real encrypted messages.
func prepareMessageUpgrade(t *testing.T, catalogDir, work string, target messageTarget) func(*messageChat, messageOps) {
	t.Helper()
	previous := os.Getenv("KYQ_TEST_MESSAGE_UPGRADE_MANIFEST")
	if previous == "" {
		return nil
	}
	path := filepath.Join(catalogDir, "kymessage", "manifest.yaml")
	candidate, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(catalogDir, "kymessage", "compose.yaml.tmpl")
	candidateCompose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.Load(os.DirFS(catalogDir))
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	// Old matrix-init cannot create files introduced by the candidate's template.
	// Install the complete historical entry, with the same explicit fixture transport.
	oldCompose, err := os.ReadFile(filepath.Join(filepath.Dir(previous), "compose.yaml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, oldCompose, 0o600); err != nil {
		t.Fatal(err)
	}
	patchMessageTransport(t, filepath.Dir(path), filepath.Join(work, "message-ca.pem"), target.forward)
	historical, err := catalog.Load(os.DirFS(catalogDir))
	if err != nil {
		t.Fatal(err)
	}
	from, to := historical["kymessage"], current["kymessage"]
	if from.Version == to.Version || !catalog.VersionAtLeast(to.Version, from.Version) || !to.Upgrade.Migrates(from.Version) {
		t.Fatal("Messages fixture must cross a real data-migration boundary")
	}
	for role, image := range from.Images {
		if role != "kymessage" && to.Images[role] != image {
			t.Fatal("Messages fixture also changes an auxiliary image", role)
		}
	}
	return func(c *messageChat, o messageOps) {
		t.Helper()
		// The later restore drill uses its own server and credentials. Separate
		// directories prevent overwriting either live server's TLS/password files.
		work := filepath.Join(o.work, "pre-upgrade-store")
		if err := os.Mkdir(work, 0o700); err != nil {
			t.Fatal(err)
		}
		srv := startRestServer(t, work, "nas")
		files := strings.TrimSuffix(c.msg.root, "/root") + "/upgrade-backup-files"
		dest := srv.destination(t, c.msg.name, files, "nas")
		docker(t, "exec", c.msg.name, "chown", "-R", "kyq", files)
		st := mustStack(t, o.plan)
		for i := range st.Targets {
			if st.Targets[i].Name == "nas" {
				st.Targets[i].Backup = dest
			}
		}
		savePlan(t, o.plan, st)
		handover := o.apply("Messages pre-upgrade backup destination")
		c.secrets["the upgrade rest-server password"] = srv.passwords["nas"]
		hashes := func() string {
			return docker(t, "exec", c.msg.name, "sha256sum",
				c.msg.root+"/kymessage/secrets/session_secret",
				c.msg.root+"/kymessage/data/installer/directory_secret",
				c.msg.root+"/kymessage"+strings.TrimPrefix(messageSetupDir, "/app")+"/client.secret",
				c.msg.root+"/kymessage/matrix/synapse/signing.key",
				c.msg.root+"/kymessage/matrix/secrets/upstream_provider_id",
				c.msg.root+"/kymessage/matrix/secrets/kyidentity_client_secret")
		}
		keys := hashes()
		c.owner.renew()
		c.member.renew()
		if err := os.WriteFile(path, candidate, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(composePath, candidateCompose, 0o600); err != nil {
			t.Fatal(err)
		}
		out := o.run(t, c.ctx, "", "upgrade", "kymessage")
		t.Logf("previous-image Messages upgrade:\n%s", out)
		if status(out, "kymessage.pre-upgrade-backup") != "ok" ||
			status(out, "kymessage.deploy") != "ok" || status(out, "kymessage.health") != "ok" ||
			!strings.Contains(out, "restic check passed") || strings.Contains(out, "operator acknowledged no pre-upgrade backup") {
			t.Fatal("Messages migration did not deploy after its own verified pre-upgrade backup")
		}
		c.secrets["restic_password"] = handoverPassword(t, c.ctx, o.state, handover, nasTarget(t, o.plan))
		pw := c.msg.root + "/kymessage/secrets/restic_password"
		snaps := snapshots(t, dest, "kymessage", pw)
		if len(snaps) != 1 || len(snaps[0].ID) != 64 {
			t.Fatal("Messages upgrade must leave one full snapshot ID")
		}
		if got := strings.Join(snaps[0].Paths, " "); got != "/kyq/dump /kyq/secrets/session_secret /kyq/volumes/backups /kyq/volumes/data /kyq/volumes/matrix /kyq/volumes/media" {
			t.Fatal("Messages pre-upgrade snapshot omitted its dump, secrets or data paths")
		}
		if !strings.Contains(out, "snapshot "+snaps[0].ID[:8]) ||
			!slices.Contains(snaps[0].Tags, "app=kymessage") ||
			!slices.Contains(snaps[0].Tags, "installation="+installationID(t, o.plan)) {
			t.Fatal("Messages upgrade did not retain its own tagged pre-upgrade snapshot")
		}
		if out, err := restic(dest, "kymessage", pw, "check"); err != nil {
			t.Fatalf("Messages pre-upgrade repository check: %v\n%s", err, out)
		}
		if got := docker(t, "inspect", "kyq-kymessage-app-1", "--format", "{{.Config.Image}}"); got != to.Images["kymessage"] {
			t.Fatal("Messages did not run the candidate image after upgrading")
		}
		if hashes() != keys {
			t.Fatal("Messages upgrade changed session/directory/OIDC/provider/signing keys")
		}
		c.owner.renew()
		c.member.renew()
		if got := c.member.read(c.room, c.event); got != c.nonce {
			t.Fatal("original device cannot decrypt old server-fetched encrypted history after upgrade")
		}
		fresh := nonce()
		members := []id.UserID{c.owner.c.UserID, c.member.c.UserID}
		event := c.owner.send(c.room, members, fresh)
		if got := c.member.read(c.room, event); got != fresh {
			t.Fatal("original devices cannot exchange a new encrypted message after upgrade")
		}
		c.console = messageLogin(t, c.ctx, c.transport, "prepared-admin", ownerPassword(t, c.ctx, c.connected, "admin.password"), true)
		messageLogin(t, c.ctx, c.transport, "prepared-user", ownerPassword(t, c.ctx, c.connected, "everyday.password"), false)
		o.checks()
		t.Logf("real Messages %s -> %s: verified own pre-upgrade snapshot, retained keys/sessions, decrypted old raw event and new encrypted message; auxiliary images unchanged", from.Version, to.Version)
	}
}
