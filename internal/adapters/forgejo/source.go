package forgejo

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// SourceSpec contains only non-secret OIDC source settings. ID is zero for a new
// source; retain the returned ID on the target before a repeat run.
type SourceSpec struct {
	ID                                                                          int64
	Name, Issuer, ClientID, AssignedClaim, AssignedValue, RolesClaim, AdminRole string
}

// SourceClient uses Forgejo's native administrator forms because its public REST
// API does not expose authentication sources. Form contracts are release-qualified.
type SourceClient struct {
	origin string
	http   *http.Client
}

func (c *Client) OpenSourceSession(ctx context.Context, login, password string) (*SourceClient, error) {
	if !username.MatchString(login) || password == "" || len(password) > 16384 || strings.ContainsAny(password, "\x00\r\n") {
		return nil, errors.New("Forgejo bootstrap credential is invalid")
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, http.MethodGet, "/version", nil, &version); err != nil {
		return nil, err
	}
	if strings.SplitN(version.Version, "+", 2)[0] != "16.0.5" {
		return nil, errors.New("Forgejo native source forms require the qualified 16.0.5 release")
	}
	client := *c.http
	client.Jar, _ = cookiejar.New(nil)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := &SourceClient{origin: c.origin, http: &client}
	page, _, err := s.request(ctx, http.MethodGet, "/user/login", nil)
	if err != nil {
		return nil, err
	}
	fields, err := nativeForm(page, "user_name")
	if err != nil {
		return nil, err
	}
	fields.Set("user_name", login)
	fields.Set("password", password)
	fields.Del("remember")
	if _, _, err := s.request(ctx, http.MethodPost, "/user/login", fields); err != nil {
		return nil, err
	}
	page, status, err := s.request(ctx, http.MethodGet, "/admin/auths/new", nil)
	if err != nil || status != http.StatusOK {
		return nil, errors.New("Forgejo bootstrap administrator session was refused")
	}
	if _, err := nativeForm(page, "oauth2_key"); err != nil {
		return nil, errors.New("Forgejo native authentication-source form is unavailable")
	}
	return s, nil
}

func (s *SourceClient) Close() { s.http.CloseIdleConnections(); s.http.Jar = nil }

func (s *SourceClient) request(ctx context.Context, method, path string, form url.Values) ([]byte, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, s.origin+path, body)
	if err != nil {
		return nil, 0, errors.New("invalid Forgejo native form request")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", s.origin)
	}
	response, err := s.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("Forgejo native form request did not complete; inspect before retrying")
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status >= 300 && status < 400 {
		location, err := url.Parse(response.Header.Get("Location"))
		base, _ := url.Parse(s.origin)
		if err != nil || location.User != nil || location.Fragment != "" || location.RawQuery != "" || location.Opaque != "" || (location.Scheme != "" && (location.Scheme != base.Scheme || location.Host == "")) || (location.Host != "" && (location.Host != base.Host || location.Scheme != base.Scheme)) || !strings.HasPrefix(location.Path, "/") {
			return nil, status, errors.New("Forgejo native form redirect refused")
		}
		return nil, status, nil
	}
	if status != http.StatusOK {
		return nil, status, &apiError{status: status}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, status, errors.New("invalid or oversized Forgejo native form")
	}
	return data, status, nil
}

func attribute(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func nativeForm(page []byte, required string) (url.Values, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, errors.New("invalid Forgejo native form")
	}
	var found url.Values
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			values := url.Values{}
			var fields func(*html.Node)
			fields = func(f *html.Node) {
				name, named := attribute(f, "name")
				if f.Type == html.ElementNode && named && f.Data == "input" {
					value, _ := attribute(f, "value")
					kind, _ := attribute(f, "type")
					if kind == "checkbox" {
						value = "false"
						if _, checked := attribute(f, "checked"); checked {
							value = "true"
						}
					}
					values.Add(name, value)
				}
				if f.Type == html.ElementNode && named && f.Data == "textarea" {
					var value strings.Builder
					for child := f.FirstChild; child != nil; child = child.NextSibling {
						if child.Type == html.TextNode {
							value.WriteString(child.Data)
						}
					}
					values.Add(name, value.String())
				}
				for child := f.FirstChild; child != nil; child = child.NextSibling {
					fields(child)
				}
			}
			fields(n)
			if values.Has(required) {
				if found != nil {
					err = errors.New("ambiguous Forgejo native form")
				}
				found = values
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if err != nil || found == nil {
		return nil, errors.New("Forgejo native form is missing or ambiguous")
	}
	for _, values := range found {
		for _, value := range values {
			if value != values[0] {
				return nil, errors.New("conflicting Forgejo native form fields")
			}
		}
	}
	return found, nil
}

func validateSource(spec SourceSpec, secret string) error {
	u, err := url.Parse(spec.Issuer)
	if spec.ID < 0 || !username.MatchString(spec.Name) || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("Forgejo requires a named source and verified HTTPS issuer")
	}
	for _, field := range []string{spec.ClientID, spec.AssignedClaim, spec.AssignedValue, spec.RolesClaim, spec.AdminRole} {
		if field == "" || len(field) > 255 || strings.ContainsAny(field, "\x00\r\n") {
			return errors.New("Forgejo OIDC source specification is invalid")
		}
	}
	if secret == "" || len(secret) > 16384 || strings.ContainsAny(secret, "\x00\r\n") {
		return errors.New("Forgejo OIDC credential is invalid")
	}
	return nil
}

func sourceFields(spec SourceSpec, secret string) url.Values {
	return url.Values{"type": {"6"}, "name": {spec.Name}, "is_active": {"true"}, "oauth2_provider": {"openidConnect"}, "oauth2_key": {spec.ClientID}, "oauth2_secret": {secret}, "open_id_connect_auto_discovery_url": {spec.Issuer + "/.well-known/openid-configuration"}, "oauth2_scopes": {"openid,email,profile"}, "oauth2_required_claim_name": {spec.AssignedClaim}, "oauth2_required_claim_value": {spec.AssignedValue}, "oauth2_group_claim_name": {spec.RolesClaim}, "oauth2_admin_group": {spec.AdminRole}}
}

func (s *SourceClient) sourceID(ctx context.Context, name string) (int64, error) {
	page, status, err := s.request(ctx, http.MethodGet, "/admin/auths", nil)
	if err != nil || status != http.StatusOK {
		return 0, errors.New("Forgejo native source listing is unavailable")
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return 0, errors.New("invalid Forgejo native source listing")
	}
	var id int64
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href, _ := attribute(n, "href")
			if strings.HasPrefix(href, "/admin/auths/") && n.FirstChild != nil && n.FirstChild.Type == html.TextNode && n.FirstChild.Data == name {
				value, parseErr := strconv.ParseInt(strings.TrimPrefix(href, "/admin/auths/"), 10, 64)
				if parseErr != nil || value <= 0 || id != 0 {
					err = errors.New("ambiguous Forgejo source identity")
				} else {
					id = value
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return id, err
}

// Inspect checks retained native source ID, all authority fields and forbidden
// mappings read-only. The client secret is compared but never returned or reported.
func (s *SourceClient) Inspect(ctx context.Context, spec SourceSpec, secret string) error {
	if err := validateSource(spec, secret); err != nil {
		return err
	}
	if spec.ID <= 0 {
		return errors.New("Forgejo authentication source is not pinned")
	}
	id, err := s.sourceID(ctx, spec.Name)
	if err != nil || id != spec.ID {
		return errors.New("Forgejo authentication source binding conflicts")
	}
	page, status, err := s.request(ctx, http.MethodGet, "/admin/auths/"+strconv.FormatInt(spec.ID, 10), nil)
	if err != nil || status != http.StatusOK {
		return errors.New("Forgejo source inspection was refused")
	}
	fields, err := nativeForm(page, "oauth2_key")
	if err != nil {
		return err
	}
	for name, values := range sourceFields(spec, secret) {
		if !fields.Has(name) || subtle.ConstantTimeCompare([]byte(fields.Get(name)), []byte(values[0])) != 1 {
			return errors.New("Forgejo native OIDC source configuration conflicts")
		}
	}
	for _, name := range []string{"skip_local_two_fa", "allow_username_change", "oauth2_use_custom_url", "oauth2_group_team_map_removal", "oauth2_dyn_group_maps_removal", "oauth2_quota_group_map_removal"} {
		if !fields.Has(name) || fields.Get(name) != "false" {
			return errors.New("Forgejo source has an unexpected authority mapping")
		}
	}
	for _, name := range []string{"oauth2_attribute_ssh_public_key", "oauth2_restricted_group", "oauth2_group_team_map", "oauth2_dyn_group_maps", "oauth2_quota_group_claim_name", "oauth2_quota_group_map"} {
		if !fields.Has(name) || fields.Get(name) != "" {
			return errors.New("Forgejo source has an unexpected authority mapping")
		}
	}
	return nil
}

// Apply creates a missing native source or inspects the retained source without
// mutation. Unpinned collisions and drift are refused, preserving existing sources.
func (s *SourceClient) Apply(ctx context.Context, spec SourceSpec, secret string) (SourceSpec, error) {
	if err := validateSource(spec, secret); err != nil {
		return SourceSpec{}, err
	}
	if spec.ID > 0 {
		return spec, s.Inspect(ctx, spec, secret)
	}
	id, err := s.sourceID(ctx, spec.Name)
	if err != nil {
		return SourceSpec{}, err
	}
	if id != 0 {
		return SourceSpec{}, errors.New("Forgejo authentication source exists without a pinned native ID")
	}
	_, status, err := s.request(ctx, http.MethodPost, "/admin/auths/new", sourceFields(spec, secret))
	if err != nil {
		return SourceSpec{}, err
	}
	if status != http.StatusSeeOther && status != http.StatusFound {
		return SourceSpec{}, errors.New("Forgejo native source creation did not complete")
	}
	id, err = s.sourceID(ctx, spec.Name)
	if err != nil || id <= 0 {
		return SourceSpec{}, errors.New("Forgejo created source could not be pinned; inspect before retrying")
	}
	spec.ID = id
	return spec, s.Inspect(ctx, spec, secret)
}
