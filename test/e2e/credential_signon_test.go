//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Follow the native HTTP sign-on protocol using installer-created accounts and
// credentials. No source session, JWT, receiver account or directory event is seeded.
func testInstallerCredentialSignOn(t *testing.T, ctx context.Context, issuerURL, notesURL, identityData, setupID string, ca []byte) {
	t.Helper()
	issuer, err := url.Parse(issuerURL)
	if err != nil {
		t.Fatal("invalid fixture issuer")
	}
	notes, err := url.Parse(notesURL)
	if err != nil {
		t.Fatal("invalid fixture destination")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("invalid fixture CA")
	}
	// Retain certificate/hostname verification; route only the two explicitly
	// owned named endpoints to their loopback fixtures without changing system DNS.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != issuer.Host && address != notes.Host {
			return nil, errors.New("unselected fixture destination")
		}
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
	t.Cleanup(transport.CloseIdleConnections)
	for _, account := range []struct {
		username, passwordFile string
		admin, ordinary        int
	}{
		{"owner-admin", "admin.password", http.StatusOK, http.StatusForbidden},
		{"owner", "everyday.password", http.StatusForbidden, http.StatusOK},
	} {
		password, err := os.ReadFile(filepath.Join(identityData, "installer", setupID, account.passwordFile))
		if err != nil || len(password) == 0 {
			t.Fatal("installer-created account credential missing")
		}
		defer clear(password)
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		send := func(method, endpoint string, body []byte, csrf string) *http.Response {
			t.Helper()
			req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
			if err != nil {
				t.Fatal("invalid native sign-on request")
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if csrf != "" {
				req.Header.Set("X-CSRF-Token", csrf)
			}
			r, err := client.Do(req)
			if err != nil {
				t.Fatal("native sign-on transport failed")
			}
			return r
		}
		csrfResponse := send("GET", issuerURL+"/api/auth/csrf", nil, "")
		var csrf struct {
			CSRFToken string `json:"csrfToken"`
		}
		err = json.NewDecoder(io.LimitReader(csrfResponse.Body, 4096)).Decode(&csrf)
		csrfResponse.Body.Close()
		if err != nil || csrfResponse.StatusCode != 200 || csrf.CSRFToken == "" {
			t.Fatal("native CSRF admission failed")
		}
		payload, err := json.Marshal(map[string]string{"username": account.username, "password": string(password)})
		if err != nil {
			t.Fatal("invalid fixture password request")
		}
		signin := send("POST", issuerURL+"/api/auth/login", payload, csrf.CSRFToken)
		clear(payload)
		var result struct{ Success, MFARequired bool }
		err = json.NewDecoder(io.LimitReader(signin.Body, 4096)).Decode(&result)
		signin.Body.Close()
		if err != nil || signin.StatusCode != 200 || !result.Success || result.MFARequired {
			t.Fatal("installer-created account password admission failed", account.username, signin.StatusCode)
		}
		start := send("GET", notesURL+"/api/v1/auth/oidc/login", nil, "")
		start.Body.Close()
		authorize, err := start.Location()
		if err != nil || start.StatusCode != 302 || authorize.Scheme != "https" || authorize.Host != issuer.Host || authorize.Path != "/oauth/authorize" {
			t.Fatal("native Notes authorization start failed", start.StatusCode)
		}
		q := authorize.Query()
		if q.Get("client_id") != "notes" || q.Get("redirect_uri") != notesURL+"/api/v1/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatal("native PKCE authorization intent missing")
		}
		authorized := send("GET", authorize.String(), nil, "")
		authorized.Body.Close()
		callback, err := authorized.Location()
		if err != nil || authorized.StatusCode != 302 || callback.Scheme != "https" || callback.Host != notes.Host || callback.Path != "/api/v1/auth/oidc/callback" || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatal("native Identity authorization failed", account.username, authorized.StatusCode)
		}
		completed := send("GET", callback.String(), nil, "")
		completed.Body.Close()
		if completed.StatusCode != 302 {
			t.Fatal("native Notes token/claims admission failed", account.username, completed.StatusCode)
		}
		for _, access := range []struct {
			path   string
			status int
		}{{"/api/v1/admin/users", account.admin}, {"/api/v1/containers", account.ordinary}} {
			r := send("GET", notesURL+access.path, nil, "")
			r.Body.Close()
			if r.StatusCode != access.status {
				t.Fatal("installer-composed application access mismatch", account.username, access.path, r.StatusCode)
			}
		}
	}
	t.Log("installer-created accounts and transferred credentials completed native CSRF/password/PKCE/token sign-on; owner administration and everyday Notes access are separated")
}
