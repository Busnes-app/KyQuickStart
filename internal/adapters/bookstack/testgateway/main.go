// Disposable native-consumer fixture only. Never deploy as an authentication service.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type grant struct{ actor, redirect, challenge string }

func main() {
	issuer, origin, backendURL := os.Getenv("FIXTURE_ISSUER"), os.Getenv("FIXTURE_ORIGIN"), os.Getenv("FIXTURE_BACKEND")
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || !strings.HasPrefix(u.Hostname(), "kyq-bookstack-") || u.Path != "/fixture-idp" {
		log.Fatal("invalid disposable issuer")
	}
	backend, err := url.Parse(backendURL)
	if err != nil || backend.Scheme != "http" || !strings.HasPrefix(backend.Hostname(), "kyq-bookstack-") {
		log.Fatal("invalid disposable backend")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal("fixture key generation failed")
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{u.Hostname()}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil || os.WriteFile("/fixture/ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644) != nil {
		log.Fatal("fixture certificate generation failed")
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backend)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}}
	var mu sync.Mutex
	codes := map[string]grant{}
	authorizations, exchanges := 0, 0
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/fixture-idp") {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"},
				"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/keys":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "fixture-key",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/actor":
			actor := r.URL.Query().Get("who")
			if actor != "owner" && actor != "daily" && actor != "unassigned" && actor != "bad-signature" &&
				actor != "daily-admin" && actor != "daily-missing" && actor != "daily-empty" && actor != "daily-unknown" &&
				actor != "daily-duplicate" && actor != "daily-malformed" && actor != "daily-string" {
				http.Error(w, "invalid fixture actor", 400)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "fixture_actor", Value: actor, Path: "/fixture-idp", Secure: true, HttpOnly: true})
			fmt.Fprint(w, `{ "ready": true }`)
		case "/authorize":
			q := r.URL.Query()
			actor, err := r.Cookie("fixture_actor")
			if err != nil || q.Get("client_id") != "fixture-client" || q.Get("redirect_uri") != origin+"/oidc/callback" ||
				q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
				http.Error(w, "invalid fixture authorization", 400)
				return
			}
			var random [16]byte
			rand.Read(random[:])
			code := hex.EncodeToString(random[:])
			codes[code] = grant{actor.Value, q.Get("redirect_uri"), q.Get("code_challenge")}
			authorizations++
			http.Redirect(w, r, origin+"/oidc/callback?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), 302)
		case "/token":
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			if r.ParseForm() != nil {
				http.Error(w, "invalid fixture token form", 400)
				return
			}
			client, secret, basic := r.BasicAuth()
			g, found := codes[r.Form.Get("code")]
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !found || !basic || client != "fixture-client" || secret != "disposable-fixture-client-secret" ||
				r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != g.redirect || g.challenge != base64.RawURLEncoding.EncodeToString(challenge[:]) {
				http.Error(w, "invalid fixture token grant", 401)
				return
			}
			delete(codes, r.Form.Get("code"))
			exchanges++
			actor := g.actor
			if actor == "bad-signature" {
				actor = "owner"
			}
			roles := []string{"editor"}
			if actor == "owner" {
				roles = []string{"admin"}
			}
			if strings.HasPrefix(actor, "daily-") {
				actor = "daily"
			}
			claims := map[string]any{"iss": issuer, "aud": "fixture-client", "sub": actor + "-subject", "email": actor + "@example.test",
				"email_verified": true, "name": actor, "roles": roles, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
			switch g.actor {
			case "daily-admin":
				claims["roles"] = []string{"admin"}
			case "daily-missing":
				delete(claims, "roles")
			case "daily-empty":
				claims["roles"] = []string{}
			case "daily-unknown":
				claims["roles"] = []string{"foreign"}
			case "daily-duplicate":
				claims["roles"] = []string{"editor", "editor"}
			case "daily-malformed":
				claims["roles"] = []any{"editor", 1}
			case "daily-string":
				claims["roles"] = "editor"
			}
			encode := func(value any) string {
				raw, _ := json.Marshal(value)
				return base64.RawURLEncoding.EncodeToString(raw)
			}
			unsigned := encode(map[string]string{"typ": "JWT", "alg": "RS256", "kid": "fixture-key"}) + "." + encode(claims)
			digest := sha256.Sum256([]byte(unsigned))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				http.Error(w, "fixture signing failed", 500)
				return
			}
			if g.actor == "bad-signature" {
				signature[0] ^= 1
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "disposable-access-token", "token_type": "Bearer", "expires_in": 60,
				"id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)})
		case "/stats":
			json.NewEncoder(w).Encode(map[string]int{"authorizations": authorizations, "exchanges": exchanges})
		default:
			http.NotFound(w, r)
		}
	})
	if target := os.Getenv("FIXTURE_IDENTITY_BACKEND"); target != "" {
		native, err := url.Parse(target)
		if err != nil || native.Scheme != "http" || !strings.HasPrefix(native.Hostname(), "kyq-bookstack-") || !strings.HasSuffix(native.Hostname(), "-identity") || native.Path != "" || native.User != nil {
			log.Fatal("invalid disposable native identity backend")
		}
		mux.Handle("/fixture-idp/", &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(native)
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/fixture-idp")
			r.Out.URL.RawPath = ""
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		}})
	} else {
		mux.Handle("/fixture-idp/", fixture)
	}
	server := &http.Server{Addr: ":8443", Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}}
	if server.ListenAndServeTLS("", "") != nil {
		log.Fatal("disposable fixture server stopped")
	}
}
