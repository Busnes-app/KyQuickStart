// Package bookstack calls the native offboarding extension with a pinned contract.
package bookstack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

const contract = "ky.bookstack-offboard/1"
const moduleVersion = "0.15.0"

// Binding comes from verified provisioning, never email-only directory events.
type Binding struct {
	ID      int64
	Issuer  string
	Subject string
}

type Account struct {
	Binding
	Disabled, RevocationComplete bool
	TokensRemaining              int64
}

type Client struct {
	exec    func(context.Context, []string, []byte) ([]byte, error)
	parent  string
	artisan string
}

// New receives an executor already bound to the owned container and application
// user. It executes argv with stdin; it never chooses or adopts a target. The parent
// directory and artisan path are canonical absolute paths inside that container.
func New(execute func(context.Context, []string, []byte) ([]byte, error), parent, artisan string) (*Client, error) {
	if execute == nil || !absolute(parent) || !absolute(artisan) {
		return nil, errors.New("BookStack requires a native executor and canonical target paths")
	}
	return &Client{exec: execute, parent: parent, artisan: artisan}, nil
}

func absolute(s string) bool {
	return path.IsAbs(s) && path.Clean(s) == s && len(s) <= 4096 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validate(b Binding) error {
	u, err := url.Parse(b.Issuer)
	if b.ID <= 0 || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(b.Issuer, "#") || u.Opaque != "" ||
		len(b.Issuer) > 2048 || !utf8.ValidString(b.Issuer) || strings.Contains(b.Issuer, `\`) || strings.IndexFunc(b.Issuer, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 ||
		b.Subject == "" || len(b.Subject) > 255 || !utf8.ValidString(b.Subject) || strings.IndexFunc(b.Subject, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return errors.New("BookStack requires an exact native ID/HTTPS issuer/subject binding")
	}
	return nil
}

func digest(b Binding) string {
	sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
	return hex.EncodeToString(sum[:])
}

func readReport(data []byte, b Binding) (Account, error) {
	var wire struct {
		Contract string `json:"contract"`
		Version  string `json:"module_version"`
		ID       int64  `json:"user_id"`
		Digest   string `json:"binding_digest"`
		Disabled *bool  `json:"disabled"`
		Complete *bool  `json:"revocation_complete"`
		Tokens   *int64 `json:"tokens_remaining"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > 16384 || decoder.Decode(&wire) != nil || wire.Contract != contract || wire.Version != moduleVersion || wire.ID != b.ID ||
		wire.Digest != digest(b) || wire.Disabled == nil || wire.Complete == nil || wire.Tokens == nil || *wire.Tokens < 0 || (*wire.Complete && !*wire.Disabled) {
		return Account{}, errors.New("invalid BookStack identity or revocation report")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Account{}, errors.New("invalid BookStack report suffix")
	}
	return Account{Binding: b, Disabled: *wire.Disabled, RevocationComplete: *wire.Complete, TokensRemaining: *wire.Tokens}, nil
}

func (c *Client) call(ctx context.Context, b Binding, action string) (Account, error) {
	if err := validate(b); err != nil {
		return Account{}, err
	}
	request, err := json.Marshal(struct {
		Contract string `json:"contract"`
		Action   string `json:"action"`
		ID       int64  `json:"user_id"`
		Issuer   string `json:"issuer"`
		Subject  string `json:"subject"`
	}{contract, action, b.ID, b.Issuer, b.Subject})
	if err != nil || len(request) > 4096 {
		return Account{}, errors.New("invalid BookStack offboarding request")
	}
	data, err := c.execute(ctx, "kyquickstart:offboard", request)
	if err != nil {
		return Account{}, err
	}
	return readReport(data, b)
}

func (c *Client) execute(ctx context.Context, command string, request []byte) ([]byte, error) {
	// Each invocation owns a fresh directory and request file. Native PHP verifies
	// the file owner, mode, canonical path and all ancestors before any mutation.
	script := `set -eu
umask 077
parent=` + remote.Quote(c.parent) + `
[ "$(cd "$parent" && pwd -P)" = "$parent" ] || exit 1
d=$(mktemp -d ` + remote.Quote(path.Join(c.parent, "kyquickstart-offboard.XXXXXXXXXX")) + `)
trap 'rm -f "$d/request.json"; rmdir "$d"' EXIT
cat > "$d/request.json"
php ` + remote.Quote(c.artisan) + ` ` + remote.Quote(command) + ` --request="$d/request.json"`
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := c.exec(callCtx, []string{"sh", "-c", script}, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("BookStack command did not complete; inspect state before retrying")
	}
	if len(data) > 16384 {
		return nil, errors.New("oversized BookStack command response")
	}
	return data, nil
}

func (c *Client) Inspect(ctx context.Context, b Binding) (Account, error) {
	return c.call(ctx, b, "check")
}

// Deprovision verifies the supported native contract before mutation, repairs
// incomplete revocation on an explicit retry, then confirms both fences and zero
// native tokens. It never reactivates an account or infers ownership from a name.
func (c *Client) Deprovision(ctx context.Context, b Binding) error {
	return deprovision(ctx, b, c.call)
}

func deprovision(ctx context.Context, b Binding, call func(context.Context, Binding, string) (Account, error)) error {
	before, err := call(ctx, b, "check")
	if err != nil {
		return err
	}
	if before.RevocationComplete && before.TokensRemaining == 0 {
		return nil
	}
	applied, err := call(ctx, b, "deprovision")
	if err != nil {
		return err
	}
	if !applied.Disabled || !applied.RevocationComplete || applied.TokensRemaining != 0 {
		return errors.New("BookStack revocation is incomplete; inspect and resume")
	}
	after, err := call(ctx, b, "check")
	if err != nil {
		return err
	}
	if !after.Disabled || !after.RevocationComplete || after.TokensRemaining != 0 {
		return errors.New("BookStack did not retain complete revocation")
	}
	return nil
}
