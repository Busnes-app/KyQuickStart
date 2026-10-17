package kube

import (
	"context"
	"errors"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// npmEmail is the installer's NPM administrator: an identifier, not a secret. The
	// password is the npm app's generated admin_password.
	npmEmail       = "admin@kyquickstart.invalid"
	npmPasswordKey = "admin_password"
	edgeNamespace  = "kyq-edge"
	edgeSecret     = "kyq-credentials"
	edgeTokenKey   = "npm_token"
)

// npmAdmin opens NPM's admin API in namespace through a port-forward to its ClusterIP Service,
// and reads the installer's administrator password, registered for redaction.
func (c *Client) npmAdmin(ctx context.Context, namespace string, redact *engine.Redactor) (*edge.Admin, string, error) {
	sec, _, err := getOwned(ctx, c.cs.CoreV1().Secrets(namespace), "secret", secretName)
	if err != nil {
		return nil, "", err
	}
	if sec == nil || len(sec.Data[npmPasswordKey]) == 0 {
		return nil, "", fmt.Errorf("secret %s/%s has no %s; run kyquickstart apply", namespace, secretName, npmPasswordKey)
	}
	password := string(sec.Data[npmPasswordKey])
	redact.Add(password)
	origin := fmt.Sprintf("kube://%s/%s:%d", namespace, appName, catalog.AdminPort)
	admin, err := edge.NewAdmin(origin, c.ServiceDialer(namespace, appName, catalog.AdminPort))
	return admin, password, err
}

// NPMAccess returns a tunnel to the in-cluster NPM at namespace/service:port and a token for
// it: the stored npm_token while NPM accepts it, else one minted in memory from the
// installer's administrator login. Only apply's bootstrap step stores tokens.
func (c *Client) NPMAccess(ctx context.Context, namespace, service string, port int) ([]byte, edge.Dial, error) {
	if want := namespaceOf("npm"); namespace != want || service != appName || port != catalog.AdminPort {
		return nil, nil, fmt.Errorf("a kube:// npm_url must name the npm app's admin Service, kube://%s/%s:%d", want, appName, catalog.AdminPort)
	}
	admin, password, err := c.npmAdmin(ctx, namespace, &engine.Redactor{})
	if err != nil {
		return nil, nil, err
	}
	defer admin.Close()
	if stored, err := c.EdgeToken(ctx); err == nil {
		ok, err := admin.Verify(ctx, string(stored))
		if err != nil {
			return nil, nil, err
		}
		if ok {
			return stored, c.ServiceDialer(namespace, service, port), nil
		}
	}
	token, err := admin.Login(ctx, npmEmail, password)
	if err == nil && token == "" {
		err = errors.New("NPM rejects the installer's administrator login; run kyquickstart apply to bootstrap it")
	}
	if err != nil {
		return nil, nil, err
	}
	return []byte(token), c.ServiceDialer(namespace, service, port), nil
}

// npmBootstrapStep replaces NPM's first administrator with the installer's (or rotates the
// default login of older data to it) and stores an API token in kyq-edge/kyq-credentials.
type npmBootstrapStep struct{ a App }

func (s npmBootstrapStep) ID() string        { return s.a.Name + ".bootstrap" }
func (s npmBootstrapStep) InputHash() string { return hashOf("npm-bootstrap", npmEmail) }

// state returns why bootstrap is not done, "" when it is.
func (s npmBootstrapStep) state(ctx context.Context) (string, error) {
	admin, _, err := s.a.Client.npmAdmin(ctx, namespaceOf(s.a.Name), s.a.Redact)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	stored, err := s.a.Client.EdgeToken(ctx)
	if err != nil {
		return "no stored NPM token", nil
	}
	s.a.Redact.Add(string(stored))
	if ok, err := admin.Verify(ctx, string(stored)); err != nil || !ok {
		return "stored NPM token is rejected", err
	}
	if def, err := admin.Login(ctx, edge.DefaultEmail, edge.DefaultPassword); err != nil || def != "" {
		return "NPM accepts its default login", err
	}
	return "", nil
}

func (s npmBootstrapStep) Inspect(ctx context.Context) (bool, error) {
	why, err := s.state(ctx)
	return why == "", err
}

func (s npmBootstrapStep) Apply(ctx context.Context) error {
	admin, password, err := s.a.Client.npmAdmin(ctx, namespaceOf(s.a.Name), s.a.Redact)
	if err != nil {
		return err
	}
	defer admin.Close()
	setup, err := admin.Setup(ctx)
	if err != nil {
		return err
	}
	if !setup {
		// No user yet: the API admits one unauthenticated creation, reachable only through
		// this port-forward (the policy admits no pod to port 81).
		if err := admin.CreateAdmin(ctx, npmEmail, password); err != nil {
			return err
		}
	}
	token, err := admin.Login(ctx, npmEmail, password)
	if err != nil {
		return err
	}
	def, err := admin.Login(ctx, edge.DefaultEmail, edge.DefaultPassword)
	if err != nil {
		return err
	}
	switch {
	case token == "" && def != "":
		if err := admin.Rotate(ctx, def, edge.DefaultPassword, npmEmail, password); err != nil {
			return err
		}
		if token, err = admin.Login(ctx, npmEmail, password); err != nil {
			return err
		}
		if token == "" {
			return errors.New("NPM rejects the installer's administrator login after rotating the default one")
		}
	case token == "":
		return errors.New("NPM rejects both the installer's administrator login and its default one; reset its administrator by hand")
	case def != "":
		return errors.New("NPM has another administrator with the default login; change or remove it in NPM, then rerun apply")
	}
	s.a.Redact.Add(token)
	return s.store(ctx, token)
}

// store writes the token into kyq-edge/kyq-credentials, keeping the Secret's other keys.
func (s npmBootstrapStep) store(ctx context.Context, token string) error {
	labels := workloadLabels(s.a.ReleaseSet)
	nsLabels := workloadLabels(s.a.ReleaseSet)
	nsLabels[podSecurity] = "restricted"
	if err := s.a.Client.ensureNamespace(ctx, edgeNamespace, nsLabels); err != nil {
		return err
	}
	api := s.a.Client.cs.CoreV1().Secrets(edgeNamespace)
	sec, found, err := getOwned(ctx, api, "secret", edgeSecret)
	if err != nil {
		return err
	}
	if !found {
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: edgeSecret, Namespace: edgeNamespace, Labels: labels}, Type: corev1.SecretTypeOpaque}
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[edgeTokenKey] = []byte(token)
	if found {
		_, err = api.Update(ctx, sec, metav1.UpdateOptions{})
	} else {
		_, err = api.Create(ctx, sec, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("secret %s/%s: %w", edgeNamespace, edgeSecret, engine.ErrTransient)
	case err != nil:
		return fmt.Errorf("secret %s/%s: cluster refused credential publication", edgeNamespace, edgeSecret)
	}
	return nil
}

func (s npmBootstrapStep) Verify(ctx context.Context) error {
	why, err := s.state(ctx)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}
