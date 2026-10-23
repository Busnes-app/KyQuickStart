package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// VerifyRoutes probes public HTTPS while injecting forged forwarding headers. A product
// must report its resolved client address, trusted-proxy decision, and resolved scheme.
// expected decides which routes this run reaches: inside allow_cidrs (this workstation's LAN
// address), the internal routes on client_port; outside, the external routes. It returns
// which of the two it verified and the hostnames it skipped: routes with no status path, whose
// product does not report the contract. Every other route is checked; the error lists each
// that failed.
func VerifyRoutes(ctx context.Context, e stack.Edge, expected netip.Addr) (string, []string, error) {
	inside := slices.ContainsFunc(e.AllowCIDRs, func(c string) bool { return netip.MustParsePrefix(c).Contains(expected.Unmap()) })
	scope, port := "external", ""
	if inside {
		scope, port = "internal", ":"+strconv.Itoa(e.ClientPort)
	}
	routes := slices.DeleteFunc(slices.Clone(e.Routes), func(r stack.Route) bool { return e.Internal(r) != inside })
	switch {
	case len(routes) > 0:
	case inside:
		return "", nil, fmt.Errorf("this workstation's address %s is inside allow_cidrs, which reach only internal routes, and this edge has none; verify-edge must run from outside them", expected)
	default:
		return "", nil, fmt.Errorf("this workstation's address %s is outside allow_cidrs; verify-edge must run from a client the edge's internal routes serve", expected)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var skipped []string
	var failed []error
	for _, r := range routes {
		if r.StatusPath == "" {
			skipped = append(skipped, r.Hostname)
			continue
		}
		if err := verifyRoute(ctx, client, "https://"+r.Hostname+port+r.StatusPath, expected); err != nil {
			failed = append(failed, fmt.Errorf("route %s: %w", r.Hostname, err))
		}
	}
	return scope, skipped, errors.Join(failed...)
}

func verifyRoute(ctx context.Context, client *http.Client, endpoint string, expected netip.Addr) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid route status URL")
	}
	spoof := "198.51.100.66"
	if expected.String() == spoof {
		spoof = "203.0.113.66"
	}
	req.Header.Set("X-Forwarded-For", spoof)
	req.Header.Set("X-Real-IP", spoof)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("X-Forwarded-Scheme", "http")
	req.Header.Set("Forwarded", "for="+spoof+";proto=http")
	// Cloudflare overwrites these; a direct edge's product must not see the forged value.
	req.Header.Set("CF-Connecting-IP", spoof)
	req.Header.Set("True-Client-IP", spoof)
	res, err := client.Do(req)
	if err != nil {
		return errors.New("public HTTPS status request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("public status endpoint returned HTTP %d", res.StatusCode)
	}
	var status struct {
		ClientIP string `json:"clientIp"`
		Trusted  bool   `json:"proxyHeadersTrusted"`
		Scheme   string `json:"scheme"`
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(b) > 64<<10 || json.Unmarshal(b, &status) != nil {
		return errors.New("product status response is invalid or exceeds 64 KiB")
	}
	ip, err := netip.ParseAddr(status.ClientIP)
	if err != nil || status.Scheme == "" {
		return errors.New("G11 requires clientIp, proxyHeadersTrusted and scheme in the product status endpoint")
	}
	if !status.Trusted || status.Scheme != "https" || ip.Unmap() != expected.Unmap() {
		return errors.New("product does not report the expected client address, trusted proxy and HTTPS scheme with forged headers present")
	}
	return nil
}
