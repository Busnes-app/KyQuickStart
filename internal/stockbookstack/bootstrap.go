package stockbookstack

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TokenName names the installer's one API token on the service account.
const TokenName = "kyquickstart"

var (
	passwordRE  = regexp.MustCompile(`^[A-Za-z0-9]{32}$`) // create-admin --generate-password
	csrfRE      = regexp.MustCompile(`<meta name="token" content="([^"]+)">`)
	tokenLinkRE = regexp.MustCompile(`/api-tokens/([0-9]+)/([0-9]+|create)\b`)
	tokenIDRE   = regexp.MustCompile(`name="token_id"[^>]*value="([A-Za-z0-9]{32})"`)
	secretRE    = regexp.MustCompile(`readonly="readonly" value="([A-Za-z0-9]{32})"`)
)

// ReadPassword reads create-admin --generate-password's output: one line, the password.
func ReadPassword(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 256))
	p := []byte(strings.TrimSuffix(string(b), "\n"))
	clear(b)
	if err != nil || !passwordRE.Match(p) {
		clear(p)
		return nil, errors.New("stdin is not create-admin's one generated password")
	}
	return p, nil
}

// session is a browser on a standard-auth BookStack.
type session struct {
	ctx  context.Context
	base string
	c    *http.Client
	csrf string
}

func (s *session) do(method, path string, form url.Values) (*http.Response, string, error) {
	var body io.Reader
	if form != nil {
		form.Set("_token", s.csrf)
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(s.ctx, method, s.base+path, body)
	if err != nil {
		return nil, "", err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := s.c.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", method, path, ErrUnavailable)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", method, path, ErrUnavailable)
	}
	if m := csrfRE.FindSubmatch(b); m != nil {
		s.csrf = string(m[1])
	}
	return res, string(b), nil
}

// get wants a 200 page.
func (s *session) get(path string) (string, error) {
	res, page, err := s.do("GET", path, nil)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s answered %d", path, res.StatusCode)
	}
	return page, nil
}

// post wants the redirect a successful form answers with, and returns its path.
func (s *session) post(path string, form url.Values) (string, error) {
	res, _, err := s.do("POST", path, form)
	if err != nil {
		return "", err
	}
	loc, lerr := res.Location()
	if res.StatusCode != http.StatusFound || lerr != nil {
		return "", fmt.Errorf("POST %s answered %d", path, res.StatusCode)
	}
	return loc.Path, nil
}

// Bootstrap signs in to a standard-auth BookStack at base as the service account, replaces
// every API token it holds with one named kyquickstart, makes Editor the default role of new
// accounts with self-registration off, and only then writes the token's id:secret to
// tokenFile (0600, atomically): a run that dies earlier leaves the old file, which the next
// run's check finds refused, so it bootstraps again.
func Bootstrap(ctx context.Context, base string, password []byte, tokenFile string) error {
	jar, _ := cookiejar.New(nil)
	s := &session{ctx: ctx, base: base, c: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if _, err := s.get("/login"); err != nil {
		return err
	}
	form := url.Values{"email": {ServiceEmail}, "password": {string(password)}}
	where, err := s.post("/login", form)
	form.Del("password")
	if err != nil || where == "/login" {
		return errors.New("the service account's sign-in was refused")
	}
	page, err := s.get("/my-account/auth")
	if err != nil {
		return err
	}
	user, tokens := "", []string{}
	for _, m := range tokenLinkRE.FindAllStringSubmatch(page, -1) {
		if user != "" && m[1] != user {
			return errors.New("the service account's token list names another account")
		}
		user = m[1]
		if m[2] != "create" && !slices.Contains(tokens, m[2]) {
			tokens = append(tokens, m[2])
		}
	}
	if user == "" {
		return errors.New("the service account cannot create API tokens")
	}
	for _, id := range tokens {
		if _, err := s.post("/api-tokens/"+user+"/"+id, url.Values{"_method": {"DELETE"}}); err != nil {
			return fmt.Errorf("delete the service account's old token: %w", err)
		}
	}
	created, err := s.post("/api-tokens/"+user+"/create", url.Values{"name": {TokenName}, "expires_at": {""}})
	if err != nil {
		return fmt.Errorf("create the service account's token: %w", err)
	}
	page, err = s.get(created)
	if err != nil {
		return err
	}
	id, secret := tokenIDRE.FindStringSubmatch(page), secretRE.FindStringSubmatch(page)
	if id == nil || secret == nil {
		return errors.New("BookStack showed no new token")
	}
	api := API{URL: base, Token: id[1] + ":" + secret[1]}
	roles, err := api.Roles(ctx)
	if err != nil {
		return err
	}
	editor := 0
	for _, r := range roles {
		if r.SystemName == "" && r.DisplayName == "Editor" {
			if editor != 0 {
				return errors.New("BookStack has two roles named Editor")
			}
			editor = r.ID
		}
	}
	if editor == 0 {
		return errors.New("BookStack has no role named Editor for new accounts")
	}
	if err := setRegistration(s, editor); err != nil {
		return err
	}
	if err := Check(ctx, api); err != nil {
		return err
	}
	// The one-off shares BookStack's session store: end this session there.
	if _, err := s.post("/logout", url.Values{}); err != nil {
		return fmt.Errorf("sign out: %w", err)
	}
	return writeToken(tokenFile, api.Token)
}

// setRegistration sets the registration form, then reads it back: Editor by default, no
// self-registration, no domain restriction, no email confirmation (sign-in is KyIdentity's).
func setRegistration(s *session, editor int) error {
	if _, err := s.get("/settings/registration"); err != nil {
		return err
	}
	if _, err := s.post("/settings/registration", url.Values{"section": {"registration"},
		"setting-registration-enabled": {"false"}, "setting-registration-role": {fmt.Sprint(editor)},
		"setting-registration-restrict": {""}, "setting-registration-confirmation": {"false"}}); err != nil {
		return fmt.Errorf("set registration: %w", err)
	}
	page, err := s.get("/settings/registration")
	if err != nil {
		return err
	}
	selected := regexp.MustCompile(`<option value="([0-9]+)"[^>]*\sselected\s*>`).FindAllStringSubmatch(page, -1)
	switch {
	case len(selected) != 1 || selected[0][1] != fmt.Sprint(editor):
		return errors.New("BookStack's registration role did not read back as Editor")
	case !strings.Contains(page, `name="setting-registration-enabled" value="false"`),
		!strings.Contains(page, `name="setting-registration-confirmation" value="false"`),
		!regexp.MustCompile(`name="setting-registration-restrict"[^>]*value=""`).MatchString(html.UnescapeString(page)):
		return errors.New("BookStack's registration settings did not read back")
	}
	return nil
}

// writeToken publishes token at file, private, by rename: a reader never sees half of it.
func writeToken(file, token string) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".token.*")
	if err != nil {
		return errors.New("cannot write the token file")
	}
	defer os.Remove(f.Name())
	w := bufio.NewWriter(f)
	w.WriteString(token)
	if w.Flush() != nil || f.Chmod(0o600) != nil || f.Sync() != nil || f.Close() != nil || os.Rename(f.Name(), file) != nil {
		f.Close()
		return errors.New("cannot write the token file")
	}
	return nil
}
