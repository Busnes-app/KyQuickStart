package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Private wire values live only during this explicit transfer, never in stack or answers.
type clientCredentials struct {
	Schema          string   `json:"schema"`
	SetupID         string   `json:"setupId"`
	IssuerURL       string   `json:"issuerUrl"`
	ClientID        string   `json:"clientId"`
	RedirectURIs    []string `json:"redirectUris"`
	DirectoryURL    string   `json:"directoryUrl,omitempty"`
	ClientSecret    string   `json:"clientSecret"`
	DirectorySecret string   `json:"directorySecret,omitempty"`
	Administrator   struct {
		Subject  string `json:"subject"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"administrator"`
}

func transferNotesCredentials(ctx context.Context, sourceExec, destinationExec identityExec, inputExec func(context.Context, []string, []byte) ([]byte, error), source, destination stack.Integration, check bool) error {
	if err := identityCapabilities(ctx, sourceExec, true, "client-credential-export"); err != nil {
		return err
	}
	if err := notesCapabilities(ctx, destinationExec, true, "client-credential-import"); err != nil {
		return err
	}
	raw, err := sourceExec(ctx, []string{"apply-setup", "--file", source.Bundle, "--export-client", destination.IdentityClient})
	defer clear(raw)
	if err != nil {
		return errors.New("private source credential export failed")
	}
	n, err := resolveNotesCredentials(raw, source, destination)
	if err != nil {
		return err
	}
	if err := stageNotes(ctx, destinationExec, inputExec, n, destination.Bundle, check); err != nil {
		return err
	}
	if err := nativeInputReceipt(ctx, inputExec, "--import-client", raw, destination.Bundle, "kynotes.client-import/1", check); err != nil {
		return errors.New("private destination credential import was not verified")
	}
	verified, err := sourceExec(ctx, []string{"apply-setup", "--file", source.Bundle, "--export-client", destination.IdentityClient})
	defer clear(verified)
	if err != nil || !bytes.Equal(raw, verified) {
		return errors.New("source credential ownership changed during transfer; destination files are retained")
	}
	return nil
}

func resolveNotesCredentials(raw []byte, source, destination stack.Integration) (*stack.NotesPreparation, error) {
	bad := errors.New("private client export does not match selected source, destination or dedicated owner")
	if len(raw) > 64<<10 || source.Identity == nil || destination.Notes == nil || destination.Notes.SSO == nil {
		return nil, bad
	}
	var frame clientCredentials
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&frame) != nil || d.Decode(new(any)) != io.EOF {
		return nil, bad
	}
	var client *stack.IdentityClient
	for i := range source.Identity.Clients {
		if source.Identity.Clients[i].ID == destination.IdentityClient {
			client = &source.Identity.Clients[i]
		}
	}
	if client == nil || frame.Schema != "kyidentity.client-credentials/1" || frame.SetupID != source.Identity.SetupID || frame.IssuerURL != destination.Notes.SSO.IssuerURL || frame.ClientID != destination.IdentityClient || !reflect.DeepEqual(frame.RedirectURIs, client.RedirectURIs) || frame.Administrator.Username != source.Identity.Admin.Username || frame.Administrator.Role != "kynotes.admin" || !productSetupID.MatchString(frame.Administrator.Subject) || !transferSecret(frame.ClientSecret) {
		return nil, bad
	}
	if client.SuiteWebhook != nil {
		if frame.DirectoryURL != client.SuiteWebhook.URL || !transferSecret(frame.DirectorySecret) {
			return nil, bad
		}
	} else if frame.DirectoryURL != "" || frame.DirectorySecret != "" {
		return nil, bad
	}
	n := *destination.Notes
	n.Admins = []stack.NotesAdmin{{Issuer: frame.IssuerURL, Subject: frame.Administrator.Subject, Username: frame.Administrator.Username}}
	return &n, nil
}

func transferSecret(s string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == s
}
