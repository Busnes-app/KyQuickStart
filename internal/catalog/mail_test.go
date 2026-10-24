package catalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

// KyPost's native mail renderings: only "receiver" publishes a port beside the web port, and
// it publishes 25 on smtp_address alone; its Rspamd is KyPost v0.4.3's pin.
func TestKyPostMailRenderings(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kypost"]
	if !a.MailModes() || a.NativeMail.MaddySHA256 != "6ea4b951f15b91fd81d98957e4d4bad7a0cec6d6e1d66b011c765cc9a14e05db" ||
		a.Images["rspamd"] != "rspamd/rspamd:4.2.2@sha256:33526f94cfbb5ab5485805ad9d6eddb6a52aca6529aad0ce99b67b01c2e756de" || len(a.Files) != 1 {
		t.Fatalf("native mail = %+v, rspamd %s, files %d", a.NativeMail, a.Images["rspamd"], len(a.Files))
	}
	if strings.Contains(string(a.Compose), "rspamd") || strings.Contains(string(a.Compose), "smtp_address") {
		t.Fatal("the ordinary rendering mentions the receiver")
	}
	native, err := a.WithMail(MailNative, MailRefs{})
	if err != nil || native.MailModes() || strings.Contains(string(native.Compose), ":25:") || !strings.Contains(string(native.Compose), `KYPOST_NATIVE_MAIL: "true"`) {
		t.Fatalf("native: %v\n%s", err, native.Compose)
	}
	refs := MailRefs{SMTPAddress: "203.0.113.9", Hostname: "mx.example.com", MaddyBinary: "/opt/maddy", TLSDir: "/srv/tls"}
	rcv, err := a.WithMail(MailReceiver, refs)
	if err != nil || strings.Count(string(rcv.Compose), ":25:") != 1 || !strings.Contains(string(rcv.Compose), `"203.0.113.9:25:2525"`) || strings.Contains(string(rcv.Compose), "{{smtp") {
		t.Fatalf("receiver: %v\n%s", err, rcv.Compose)
	}
	if _, err := a.WithMail(MailNative, refs); err == nil {
		t.Error("native took receiver references")
	}
	if _, err := a.WithMail(MailReceiver, MailRefs{}); err == nil {
		t.Error("receiver without references")
	}
	if _, err := cat["kydrive"].WithMail(MailNative, MailRefs{}); err == nil {
		t.Error("an app without native_mail rendered native")
	}
}

func TestLoadRejectsMail(t *testing.T) {
	const native = "native_mail:\n  maddy_sha256: " + "6ea4b951f15b91fd81d98957e4d4bad7a0cec6d6e1d66b011c765cc9a14e05db" + "\n"
	const port = `    ports: ["{{smtp_address}}:25:2525"]` + "\n"
	const recv = "{{if eq .Mail \"receiver\"}}" + port + "    volumes: [\"{{maddy_binary}}:/m:ro\", \"{{receiving_tls}}:/t:ro\"]\n    environment:\n      H: \"{{mail_hostname}}\"\n{{end}}"
	for name, c := range map[string]struct {
		manifest, compose string
		files             fstest.MapFS
		want              string
	}{
		"ref outside receiver": {manifest + native, compose + port, nil, "belongs in the receiver rendering"},
		"ref without mail":     {manifest, compose + port, nil, "belongs in the receiver rendering"},
		"receiver lacks a ref": {manifest + native, compose + "{{if eq .Mail \"receiver\"}}" + port + "{{end}}", nil, "must write every mail reference"},
		"bad hash":             {manifest + "native_mail:\n  maddy_sha256: abc\n", compose, nil, "native_mail needs maddy_sha256"},
		"unbound file":         {manifest + native, compose + recv, fstest.MapFS{"a/files/conf/x.conf": {Data: []byte("x")}}, "files/conf/x.conf is bound by no service"},
		"file outside bind":    {manifest + native, compose + recv, fstest.MapFS{"a/files/x.conf": {Data: []byte("x")}}, "inside a bind directory"},
		"file bound by native": {manifest + native, compose + "    volumes: [./conf/x.conf:/x:ro]\n" + recv, fstest.MapFS{"a/files/conf/x.conf": {Data: []byte("x")}}, "only the receiver rendering binds files/conf/x.conf"},
	} {
		fsys := fstest.MapFS{"a/manifest.yaml": {Data: []byte(c.manifest)}, "a/compose.yaml.tmpl": {Data: []byte(c.compose)}}
		for k, v := range c.files {
			fsys[k] = v
		}
		if _, err := Load(fsys); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	fsys := fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest + native)}, "a/compose.yaml.tmpl": {Data: []byte(compose + strings.Replace(recv, "    volumes: [", "    volumes: [./conf/x.conf:/x:ro, ", 1))},
		"a/files/conf/x.conf": {Data: []byte("x")}}
	cat, err := Load(fsys)
	if err != nil || string(cat["a"].Files["conf/x.conf"]) != "x" || len(cat["a"].Nested) != 0 {
		t.Fatalf("bound file: %v", err)
	}
	r, _ := cat["a"].WithMail(MailReceiver, MailRefs{SMTPAddress: "203.0.113.9", Hostname: "h.example", MaddyBinary: "/m", TLSDir: "/t"})
	if len(r.Nested) != 1 || r.Nested[0] != "./conf/x.conf" {
		t.Fatalf("receiver nested = %v", r.Nested)
	}
}
