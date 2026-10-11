// Package librechat consumes the community native inspection command. It does
// not enable deployment or retained-account revocation for stock LibreChat.
package librechat

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
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

const inspectionContract = "ky.librechat-account-inspect/1"
const moduleVersion = "0.1.0"

type Binding struct{ ID, Issuer, Subject string }
type Account struct {
	Binding
	Role string
}

type Client struct {
	execute         func(context.Context, []string, []byte) ([]byte, error)
	parent, command string
}

func cleanPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && len(value) <= 4096 &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// New is bound to an explicit container/application user by its executor. The
// parent is a protected staging directory and command is the native module CLI.
func New(execute func(context.Context, []string, []byte) ([]byte, error), parent, command string) (*Client, error) {
	if execute == nil || !cleanPath(parent) || !cleanPath(command) {
		return nil, errors.New("LibreChat requires a native executor and canonical target paths")
	}
	return &Client{execute: execute, parent: parent, command: command}, nil
}

var nativeID = regexp.MustCompile(`^[0-9a-f]{24}$`)

func validBinding(b Binding) bool {
	u, err := url.Parse(b.Issuer)
	badText := func(s string) bool {
		return !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
		}) >= 0
	}
	return nativeID.MatchString(b.ID) && err == nil && u.Scheme == "https" && u.Hostname() != "" &&
		u.User == nil && u.Opaque == "" && !strings.ContainsAny(b.Issuer, "\\?#") &&
		len(b.Issuer) <= 2048 && !badText(b.Issuer) && b.Subject != "" && len(b.Subject) <= 255 && !badText(b.Subject)
}

func bindingDigest(b Binding) string {
	sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
	return hex.EncodeToString(sum[:])
}

func decode(data []byte, out any) bool {
	if len(data) == 0 || len(data) > 16384 {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil && d.Decode(new(any)) == io.EOF
}

func readReport(data []byte, b Binding) (Account, error) {
	var report struct {
		Contract string `json:"contract"`
		Version  string `json:"module_version"`
		ID       string `json:"user_id"`
		Digest   string `json:"binding_digest"`
		Role     string `json:"role"`
	}
	if !decode(data, &report) || report.Contract != "ky.librechat-account-report/1" ||
		report.Version != moduleVersion || report.ID != b.ID || report.Digest != bindingDigest(b) ||
		(report.Role != "ADMIN" && report.Role != "USER") {
		return Account{}, errors.New("invalid LibreChat native identity report")
	}
	return Account{Binding: b, Role: report.Role}, nil
}

func (c *Client) run(ctx context.Context, argv []string, input []byte) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := c.execute(callCtx, argv, input)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("LibreChat native inspection did not complete")
	}
	return data, nil
}

func (c *Client) Inspect(ctx context.Context, b Binding) (Account, error) {
	if !validBinding(b) {
		return Account{}, errors.New("LibreChat requires an exact native ID/HTTPS issuer/subject binding")
	}
	data, err := c.run(ctx, []string{"node", c.command, "--capabilities"}, nil)
	if err != nil {
		return Account{}, err
	}
	var capabilities struct {
		Contract string   `json:"contract"`
		Version  string   `json:"module_version"`
		Features []string `json:"features"`
	}
	if !decode(data, &capabilities) || capabilities.Contract != inspectionContract || capabilities.Version != moduleVersion ||
		len(capabilities.Features) != 1 || capabilities.Features[0] != "account-inspection" {
		return Account{}, errors.New("LibreChat native inspection capability is unsupported")
	}
	request, err := json.Marshal(struct {
		Contract string `json:"contract"`
		ID       string `json:"user_id"`
		Issuer   string `json:"issuer"`
		Subject  string `json:"subject"`
	}{inspectionContract, b.ID, b.Issuer, b.Subject})
	if err != nil || len(request) > 4096 {
		return Account{}, errors.New("invalid LibreChat inspection request")
	}
	script := `set -eu
umask 077
parent=` + remote.Quote(c.parent) + `
[ "$(cd "$parent" && pwd -P)" = "$parent" ] || exit 1
d=$(mktemp -d ` + remote.Quote(path.Join(c.parent, "kyquickstart-inspect.XXXXXXXXXX")) + `)
trap 'rm -f "$d/request.json"; rmdir "$d"' EXIT
cat > "$d/request.json"
node ` + remote.Quote(c.command) + ` --file "$d/request.json"`
	data, err = c.run(ctx, []string{"sh", "-c", script}, request)
	if err != nil {
		return Account{}, err
	}
	return readReport(data, b)
}
