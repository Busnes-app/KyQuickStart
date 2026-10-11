package immich

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

// ProviderSpec is protected target-local intent, including the client secret.
// KyIdentity must enforce Assigned users only; autoRegister is native first-login
// account creation, not directory provisioning or proof of lifecycle integration.
type ProviderSpec struct {
	Issuer, ClientID, ClientSecret, RolesClaim string
}

var configDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var claimName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// secretPaths are the v3.2.0 system-config credentials (server/src/dtos/config.dto.ts).
// The stored fingerprint excludes them so it is not an offline guessing oracle.
var secretPaths = [][]string{{"oauth", "clientSecret"}, {"notifications", "smtp", "transport", "password"}}

// withoutSecret returns m with path removed, copying only the maps along it.
func withoutSecret(m map[string]any, path []string) map[string]any {
	if _, ok := m[path[0]]; !ok {
		return m
	}
	out := maps.Clone(m)
	if len(path) == 1 {
		delete(out, path[0])
	} else if child, ok := m[path[0]].(map[string]any); ok {
		out[path[0]] = withoutSecret(child, path[1:])
	}
	return out
}

func providerDigest(config map[string]any) (string, error) {
	for _, path := range secretPaths {
		config = withoutSecret(config, path)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return "", errors.New("invalid Immich configuration")
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (c *Client) providerConfig(ctx context.Context) (map[string]any, map[string]any, error) {
	if err := c.qualifiedVersion(ctx); err != nil {
		return nil, nil, err
	}
	var config map[string]any
	if err := c.call(ctx, http.MethodGet, "/system-config", nil, &config); err != nil {
		return nil, nil, err
	}
	oauth, ok := config["oauth"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("Immich provider configuration unavailable")
	}
	for _, name := range []string{"enabled", "autoRegister", "allowInsecureRequests"} {
		if _, ok := oauth[name].(bool); !ok {
			return nil, nil, errors.New("Immich provider configuration incomplete")
		}
	}
	for _, name := range []string{"issuerUrl", "clientId", "clientSecret", "roleClaim", "scope", "signingAlgorithm", "tokenEndpointAuthMethod"} {
		if _, ok := oauth[name].(string); !ok {
			return nil, nil, errors.New("Immich provider configuration incomplete")
		}
	}
	return config, oauth, nil
}

// InspectProviderConfiguration returns a fingerprint, never the configuration
// or its secrets. Persist this preflight proof with intent on the target.
func (c *Client) InspectProviderConfiguration(ctx context.Context) (string, error) {
	config, _, err := c.providerConfig(ctx)
	if err != nil {
		return "", err
	}
	return providerDigest(config)
}

// ConfigureProvider refuses replacement of an existing provider and preflight
// drift. Callers must serialize ALL configuration writers: the upstream PUT is
// a full-document write without conditional-update support. Persist intent before
// calling; uncertain outcomes require readback before any further mutation.
// The returned fingerprint is the verified post-write configuration, not an
// installation ownership receipt or proof of dedicated administrator separation.
func (c *Client) ConfigureProvider(ctx context.Context, expected string, spec ProviderSpec) (string, error) {
	if !configDigest.MatchString(expected) || !https(spec.Issuer, false) || !visible(spec.ClientID, 255) ||
		strings.IndexFunc(spec.ClientID, unicode.IsSpace) >= 0 || !visible(spec.ClientSecret, 16384) || !claimName.MatchString(spec.RolesClaim) {
		return "", errors.New("Immich requires protected provider intent and preflight proof")
	}
	config, oauth, err := c.providerConfig(ctx)
	if err != nil {
		return "", err
	}
	digest, err := providerDigest(config)
	if err != nil || digest != expected {
		return "", errors.New("Immich configuration changed since preflight")
	}
	desired := map[string]any{
		"enabled": true, "autoRegister": true, "allowInsecureRequests": false,
		"issuerUrl": spec.Issuer, "clientId": spec.ClientID, "clientSecret": spec.ClientSecret,
		"roleClaim": spec.RolesClaim, "scope": "openid email profile", "signingAlgorithm": "RS256",
		"tokenEndpointAuthMethod": "client_secret_post",
	}
	matched := true
	for key, value := range desired {
		matched = matched && reflect.DeepEqual(oauth[key], value)
	}
	if matched {
		return digest, nil
	}
	if oauth["enabled"] != false || oauth["issuerUrl"] != "" || oauth["clientId"] != "" || oauth["clientSecret"] != "" {
		return "", errors.New("Immich existing provider requires explicit migration")
	}
	for key, value := range desired {
		oauth[key] = value
	}
	var ignored json.RawMessage
	if c.call(ctx, http.MethodPut, "/system-config", config, &ignored) != nil {
		return "", ErrUncertain
	}
	after, _, err := c.providerConfig(ctx)
	if err != nil || !reflect.DeepEqual(after, config) {
		return "", ErrUncertain
	}
	return providerDigest(after)
}
