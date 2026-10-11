package forgejo

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureOIDC exercises the native consumer with signed tokens and PKCE; it is
// deliberately confined to ephemeral loopback endpoints and disposable secrets.
type fixtureOIDC struct {
	server *httptest.Server
	cert   []byte
	caFile string
	mu     sync.Mutex
	who    string
	codes  map[string]map[string]any
	tokens map[string]map[string]any
	seq    int
	key    *rsa.PrivateKey
	native *nativeIdentityFixture
}

func newFixtureOIDC(t *testing.T, backend ...*url.URL) *fixtureOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureOIDC{cert: der, codes: map[string]map[string]any{}, tokens: map[string]map[string]any{}, key: key}
	var handler http.Handler = http.HandlerFunc(f.serve)
	if len(backend) == 1 {
		proxy := httputil.NewSingleHostReverseProxy(backend[0])
		director := proxy.Director
		proxy.Director = func(r *http.Request) {
			director(r)
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		handler = proxy
	}
	f.server = httptest.NewUnstartedServer(handler)
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	f.caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(f.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixtureOIDC) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	issuer := f.server.URL
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "userinfo_endpoint": issuer + "/userinfo", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
	case "/keys":
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "fixture-key", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/authorize":
		q := r.URL.Query()
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || redirect.Scheme != "https" || redirect.Hostname() != "127.0.0.1" || q.Get("client_id") != "fixture-client" || q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "invalid fixture authorization", http.StatusBadRequest)
			return
		}
		f.seq++
		code := fmt.Sprintf("fixture-code-%d", f.seq)
		f.codes[code] = map[string]any{"who": f.who, "nonce": q.Get("nonce"), "challenge": q.Get("code_challenge"), "redirect": redirect.String()}
		values := redirect.Query()
		values.Set("code", code)
		values.Set("state", q.Get("state"))
		redirect.RawQuery = values.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid fixture form", http.StatusBadRequest)
			return
		}
		client, secret, basic := r.BasicAuth()
		if !basic {
			client, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		code := r.Form.Get("code")
		grant, found := f.codes[code]
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !found || client != "fixture-client" || secret != "disposable-fixture-client-secret" || r.Form.Get("grant_type") != "authorization_code" || grant["redirect"] != r.Form.Get("redirect_uri") || grant["challenge"] != base64.RawURLEncoding.EncodeToString(challenge[:]) {
			http.Error(w, "invalid fixture grant", http.StatusUnauthorized)
			return
		}
		delete(f.codes, code)
		who := grant["who"].(string)
		claims := map[string]any{"iss": issuer, "aud": "fixture-client", "sub": who + "-subject", "iat": time.Now().Unix(), "exp": time.Now().Add(2 * time.Minute).Unix(), "nonce": grant["nonce"], "email": who + "@example.test", "email_verified": true, "preferred_username": who, "name": who, "roles": []string{"user"}}
		if who == "owner" {
			claims["roles"] = []string{"admin"}
		}
		if who != "unassigned" {
			claims["kyq_assigned"] = "forgejo"
		}
		encode := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
		unsigned := encode(map[string]string{"typ": "JWT", "alg": "RS256", "kid": "fixture-key"}) + "." + encode(claims)
		digest := sha256.Sum256([]byte(unsigned))
		signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "fixture signing failure", http.StatusInternalServerError)
			return
		}
		access := "fixture-access-" + code
		f.tokens[access] = claims
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 120, "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)})
	case "/userinfo":
		claims, found := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !found {
			http.Error(w, "invalid fixture access", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(claims)
	default:
		http.NotFound(w, r)
	}
}

func (f *fixtureOIDC) login(t *testing.T, app *httptest.Server, who string) *http.Client {
	t.Helper()
	f.mu.Lock()
	f.who = who
	f.mu.Unlock()
	baseClient := *app.Client()
	client := &baseClient
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
	if !transport.TLSClientConfig.RootCAs.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.cert})) {
		t.Fatal("fixture CA could not be installed")
	}
	client.Transport = transport
	client.Timeout = 15 * time.Second
	client.Jar, _ = cookiejar.New(nil)
	t.Cleanup(client.CloseIdleConnections)
	if f.native != nil {
		f.native.login(t, client, f.server.URL, who)
	}
	response, err := client.Get(app.URL + "/user/oauth2/kyidentity-fixture")
	if err != nil {
		t.Fatal("native OIDC fixture login did not complete")
	}
	response.Body.Close()
	f.mu.Lock()
	if f.native != nil {
		t.Logf("native KyIdentity login %s: status=%d path=%s", who, response.StatusCode, response.Request.URL.Path)
	} else {
		t.Logf("signed fixture login %s: status=%d path=%s authorizations=%d tokens=%d", who, response.StatusCode, response.Request.URL.Path, f.seq, len(f.tokens))
	}
	f.mu.Unlock()
	if response.StatusCode >= 500 {
		t.Fatalf("native OIDC fixture returned %d", response.StatusCode)
	}
	return client
}

func (f *fixtureOIDC) subject(who string) string {
	if f.native != nil {
		return f.native.subjects[who]
	}
	return who + "-subject"
}
