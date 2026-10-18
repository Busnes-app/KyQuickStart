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
	if destination.Notes == nil || destination.Notes.SSO == nil {
		return nil, errBadExport
	}
	frame, err := readClientCredentials(raw, source, destination.IdentityClient, destination.Notes.SSO.IssuerURL)
	if err != nil || frame.Administrator.Role != "kynotes.admin" {
		return nil, errBadExport
	}
	n := *destination.Notes
	n.Admins = []stack.NotesAdmin{{Issuer: frame.IssuerURL, Subject: frame.Administrator.Subject, Username: frame.Administrator.Username}}
	return &n, nil
}

var errBadExport = errors.New("private client export does not match selected source, destination or dedicated owner")

// readClientCredentials strictly decodes source's private export of clientID and checks it
// against the saved intent, the expected issuer and the owner's dedicated admin account.
func readClientCredentials(raw []byte, source stack.Integration, clientID, issuer string) (clientCredentials, error) {
	var frame clientCredentials
	if len(raw) > 64<<10 || source.Identity == nil {
		return frame, errBadExport
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&frame) != nil || d.Decode(new(any)) != io.EOF {
		return frame, errBadExport
	}
	var client *stack.IdentityClient
	for i := range source.Identity.Clients {
		if source.Identity.Clients[i].ID == clientID {
			client = &source.Identity.Clients[i]
		}
	}
	if client == nil || frame.Schema != "kyidentity.client-credentials/1" || frame.SetupID != source.Identity.SetupID || frame.IssuerURL != issuer || frame.ClientID != clientID || !reflect.DeepEqual(frame.RedirectURIs, client.RedirectURIs) || frame.Administrator.Username != source.Identity.Admin.Username || frame.Administrator.Role != client.AdministratorRole || !productSetupID.MatchString(frame.Administrator.Subject) || !transferSecret(frame.ClientSecret) {
		return frame, errBadExport
	}
	if client.SuiteWebhook != nil {
		if frame.DirectoryURL != client.SuiteWebhook.URL || !transferSecret(frame.DirectorySecret) {
			return frame, errBadExport
		}
	} else if frame.DirectoryURL != "" || frame.DirectorySecret != "" {
		return frame, errBadExport
	}
	return frame, nil
}

func transferSecret(s string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == s
}
