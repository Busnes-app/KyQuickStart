package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

// AC7: media_address is one unicast IPv4 address.
func TestMediaAddress(t *testing.T) {
	plan := "version: 1\ntargets:\n- {name: nas, host: nas.example.test, user: kyq}\napps:\n- {name: kymessage, target: nas, media_address: %s}\n"
	if _, err := Parse([]byte(strings.Replace(plan, "%s", "192.168.1.91", 1))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0.0.0.0", "127.0.0.1", "169.254.1.2", "224.0.0.1", "255.255.255.255", "'::1'", "'fd00::1'", "nas.example.test", "192.168.1.0/24", "'192.168.001.091'"} {
		_, err := Parse([]byte(strings.Replace(plan, "%s", bad, 1)))
		if err == nil || !strings.Contains(err.Error(), "media_address") {
			t.Errorf("media_address %s: %v", bad, err)
		}
	}
}

// AC7: a route role is a role name; chat_client comes with identity_client.
func TestRouteRoleAndChatClient(t *testing.T) {
	route := "version: 1\ntargets:\n- {name: nas, host: nas.example.test, user: kyq}\napps:\n- {name: kymessage, target: nas}\nedges:\n- target: nas\n  provider: lan\n  npm_url: https://npm.example.test\n  allow_cidrs: [192.168.1.0/24]\n  routes:\n  - {app: kymessage, role: %s, hostname: matrix.example.test, forward_host: 192.168.1.91, forward_port: 18008, certificate_id: 1}\n"
	if _, err := Parse([]byte(strings.Replace(route, "%s", "matrix", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.Replace(route, "%s", "Matrix", 1))); err == nil || !strings.Contains(err.Error(), "role") {
		t.Errorf("bad role: %v", err)
	}
	chat := strings.Replace(route, "%s", "matrix", 1) + "integrations:\n- {product: kymessage, target: nas, bundle: /app/data/installer/9b2c1d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e/setup.json, identity_client: kymessage, chat_client: %s}\n"
	if _, err := Parse([]byte(strings.Replace(chat, "%s", "kymessage-chat", 1))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"KyMessage", "'bad id'"} {
		if _, err := Parse([]byte(strings.Replace(chat, "%s", bad, 1))); err == nil || !strings.Contains(err.Error(), "chat_client") {
			t.Errorf("chat_client %s: %v", bad, err)
		}
	}
}

// AC7, AC13: identity.mail is TLS or STARTTLS only, its password a workstation file whose
// path never reaches the bundle.
func TestIdentityMail(t *testing.T) {
	mail := "    mail:\n      host: smtp.example.test\n      port: 587\n      security: starttls\n      from: KyIdentity <identity@example.test>\n      username: kyidentity\n      password_file: /home/owner/smtp.password\n"
	plan := strings.Replace(identityPreparationPlan, "    clients:\n", mail+"    clients:\n", 1)
	st, err := Parse([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	m := st.Integrations[0].Identity.Mail
	m.TargetFile = "/data/installer/smtp.password"
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(m)
	b := strings.TrimSpace(buf.String())
	if b != `{"host":"smtp.example.test","port":587,"security":"starttls","from":"KyIdentity <identity@example.test>","username":"kyidentity","passwordFile":"/data/installer/smtp.password"}` {
		t.Fatalf("bundle mail = %s", b)
	}
	for bad, want := range map[string]string{
		"security: starttls":                       "security: none",
		"port: 587":                                "port: 0",
		"host: smtp.example.test":                  "host: 'smtp example'",
		"password_file: /home/owner/smtp.password": "password_file: smtp.password",
		"      username: kyidentity\n":             "",
		"from: KyIdentity <identity@example.test>": "from: nobody",
	} {
		_, err := Parse([]byte(strings.Replace(plan, bad, want, 1)))
		if err == nil || !strings.Contains(err.Error(), "identity.mail") {
			t.Errorf("%q -> %q: %v", bad, want, err)
		}
	}
}
