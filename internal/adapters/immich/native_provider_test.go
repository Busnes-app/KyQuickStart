package immich

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
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic signed provider confined to an owned Docker network. Private TLS
// material is passed through a protected disposable target file, never argv.
type nativeProviderConfig struct {
	Issuer           string
	Certificate, Key []byte
	IdentityBackend  string
}

func providerMaterial(t *testing.T, hostname string) (nativeProviderConfig, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{hostname}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return nativeProviderConfig{Issuer: "https://" + hostname + ":4443", Certificate: der, Key: x509.MarshalPKCS1PrivateKey(key)}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func TestNativeContainerProvider(t *testing.T) {
	file := os.Getenv("KYQ_IMMICH_PROVIDER_FILE")
	if file == "" {
		t.Skip("owned private provider container only")
	}
	st, err := os.Stat(file)
	if err != nil || !filepath.IsAbs(file) || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 16384 {
		t.Fatal("invalid private provider material")
	}
	raw, err := os.ReadFile(file)
	var cfg nativeProviderConfig
	if err != nil || json.Unmarshal(raw, &cfg) != nil || !strings.HasPrefix(cfg.Issuer, "https://kyq-immich-") || !strings.HasSuffix(cfg.Issuer, "-provider:4443") {
		t.Fatal("invalid owned provider scope")
	}
	key, err := x509.ParsePKCS1PrivateKey(cfg.Key)
	if err != nil {
		t.Fatal("invalid private provider key")
	}
	issuer := cfg.Issuer
	claims := func(actor string) map[string]any {
		return map[string]any{"iss": issuer, "sub": "owned-" + actor, "aud": "fixture-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "name": actor, "email": actor + "@example.test", "email_verified": true, "preferred_username": actor, "roles": []string{map[string]string{"owner": "admin", "daily": "user", "unprovisioned": "user"}[actor]}}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "userinfo_endpoint": issuer + "/userinfo", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "owned-fixture", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			if r.ParseForm() != nil || r.Form.Get("client_id") != "fixture-client" || r.Form.Get("client_secret") != "disposable-client-secret" || r.Form.Get("code_verifier") != strings.Repeat("a", 43) || (r.Form.Get("code") != "owner" && r.Form.Get("code") != "daily" && r.Form.Get("code") != "unprovisioned") {
				w.WriteHeader(400)
				return
			}
			actor := r.Form.Get("code")
			token, err := signedNativeToken(key, claims(actor))
			if err != nil {
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-" + actor, "token_type": "Bearer", "expires_in": 60, "id_token": token})
		case "/userinfo":
			actor := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer access-")
			if actor != "owner" && actor != "daily" && actor != "unprovisioned" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(claims(actor))
		default:
			w.WriteHeader(404)
		}
	})
	var provider http.Handler = handler
	if cfg.IdentityBackend != "" {
		backend, err := url.Parse(cfg.IdentityBackend)
		if err != nil || backend.Scheme != "http" || backend.Host != strings.TrimSuffix(strings.TrimPrefix(issuer, "https://"), "-provider:4443")+"-identity:8080" || backend.Path != "" || backend.User != nil || backend.RawQuery != "" || backend.Fragment != "" {
			t.Fatal("invalid owned identity proxy scope")
		}
		proxy := httputil.NewSingleHostReverseProxy(backend)
		director := proxy.Director
		proxy.Director = func(r *http.Request) {
			director(r)
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "owned identity unavailable", http.StatusServiceUnavailable)
		}
		provider = proxy
	}
	server := &http.Server{Addr: ":4443", Handler: provider, ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{cfg.Certificate}, PrivateKey: key}}}}
	if err := server.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
		t.Fatal("private provider server stopped")
	}
}

func signedNativeToken(key *rsa.PrivateKey, claims any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "owned-fixture"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
