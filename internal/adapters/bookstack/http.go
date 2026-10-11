package bookstack

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var errHTTPRefused = errors.New("BookStack refused scoped request")

// HTTPClient reaches only the product-owned, narrowly scoped offboarding endpoint.
// Provisioning stays on the container-local command; no content/admin API authority exists.
type HTTPClient struct {
	origin, token string
	http          *http.Client
}

func NewHTTP(origin, token string) (*HTTPClient, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(origin, "#") || parsed.Opaque != "" {
		return nil, errors.New("BookStack requires a verified HTTPS origin")
	}
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 || strings.ToLower(token) != token {
		return nil, errors.New("BookStack scoped credential is missing or invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &HTTPClient{origin: origin, token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}, nil
}

func (c *HTTPClient) call(ctx context.Context, b Binding, action string) (Account, error) {
	if err := validate(b); err != nil {
		return Account{}, err
	}
	body, err := json.Marshal(struct {
		Contract string `json:"contract"`
		Action   string `json:"action"`
		ID       int64  `json:"user_id"`
		Issuer   string `json:"issuer"`
		Subject  string `json:"subject"`
	}{contract, action, b.ID, b.Issuer, b.Subject})
	if err != nil || len(body) > 4096 {
		return Account{}, errors.New("invalid BookStack offboarding request")
	}
	data, err := c.send(ctx, "/api/kyquickstart/offboard", body)
	if err != nil {
		return Account{}, err
	}
	return readReport(data, b)
}

func (c *HTTPClient) send(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid BookStack request")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, errors.New("BookStack request did not complete; inspect state before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode < 500 {
			return nil, errHTTPRefused
		}
		return nil, errors.New("BookStack scoped outcome unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 {
		return nil, errors.New("invalid or oversized BookStack response")
	}
	return data, nil
}

func (c *HTTPClient) Inspect(ctx context.Context, b Binding) (Account, error) {
	return c.call(ctx, b, "check")
}
func (c *HTTPClient) Deprovision(ctx context.Context, b Binding) error {
	return deprovision(ctx, b, c.call)
}
