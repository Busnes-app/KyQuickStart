package stack

import (
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// notesBundle is where the catalog KyNotes, data in /data, stages its bundle.
const notesBundle = "/data/installer/setup.json"

var containerRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
var kubeObjectRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
var kubeLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func (s *Stack) validateIntegrations() error {
	if len(s.Integrations) > 64 {
		return errors.New("at most 64 product integrations are supported")
	}
	products, workloads, amendments := map[string]bool{}, map[[4]string]bool{}, map[string]bool{}
	for _, i := range s.Integrations {
		t, found := s.Target(i.Target)
		selected, installed := false, i.Installed(s)
		for _, e := range s.Existing {
			selected = selected || e.Product == i.Product && e.Target == i.Target
		}
		if !found || !ValidName(i.Product) || !(selected || installed) || products[i.Product] {
			return errors.New("integration must select one existing or installed product on its declared target")
		}
		if !preparationPath(i.Bundle) {
			return errors.New("integration needs a clean absolute container-local bundle path")
		}
		if installed {
			// apply finds the installed app's own pod; a named workload could be another's.
			if i.Container != "" || i.Namespace != "" || i.Pod != "" {
				return errors.New("an installed app's integration names no container, namespace or pod")
			}
		} else if !containerRE.MatchString(i.Container) {
			return errors.New("integration needs a valid container")
		} else if t.Kubernetes() {
			if !kubeLabelRE.MatchString(i.Namespace) || len(i.Namespace) > 63 || !kubeObjectRE.MatchString(i.Pod) || len(i.Pod) > 253 || !kubeLabelRE.MatchString(i.Container) || len(i.Container) > 63 {
				return errors.New("Kubernetes integration needs valid namespace, pod and container names")
			}
		} else if i.Namespace != "" || i.Pod != "" {
			return errors.New("Docker integration takes no namespace or pod")
		}
		if i.Identity != nil {
			if i.Product != "kyidentity" || i.Notes != nil {
				return errors.New("Identity preparation requires only the KyIdentity product")
			}
			if err := i.Identity.validate(i.Bundle); err != nil {
				return err
			}
		}
		if i.Notes != nil {
			if i.Product != "kynotes" || path.Base(i.Bundle) != "setup.json" || path.Base(path.Dir(i.Bundle)) != "installer" {
				return errors.New("KyNotes preparation requires its product-owned installer/setup.json path")
			}
			if err := i.Notes.validate(i.Bundle); err != nil {
				return err
			}
		}
		// apply sets up an installed Ky app other than KyIdentity through its KyIdentity client.
		// KyNotes stages and imports only at its fixed path; apply derives its settings.
		if installed && i.Product != "kyidentity" {
			ok := i.Notes == nil && i.Identity == nil && identitySetupName.MatchString(i.IdentityClient)
			switch {
			case i.Product == "kynotes" && (!ok || i.Bundle != notesBundle):
				return errors.New("an installed kynotes takes only identity_client and bundle: " + notesBundle + "; apply derives its settings")
			case i.Product != "kynotes" && (!ok || i.SetupID() == ""):
				return errors.New("an installed app's setup needs its identity_client and a bundle at <data>/installer/<setup UUID>/setup.json")
			}
		}
		if i.IdentityAmendment != "" {
			id, err := uuid.Parse(i.IdentityAmendment)
			if !installed || i.Product == "kyidentity" || err != nil || id.String() != i.IdentityAmendment || i.IdentityAmendment == i.SetupID() || amendments[i.IdentityAmendment] {
				return errors.New("identity_amendment needs an installed app's integration and a canonical setup UUID of its own")
			}
			amendments[i.IdentityAmendment] = true
		}
		workload := [4]string{i.Target, i.Namespace, i.Pod, i.Container}
		if !installed && workloads[workload] {
			return errors.New("a container can have only one product integration")
		}
		products[i.Product], workloads[workload] = true, !installed
	}
	for _, i := range s.Integrations {
		if i.Identity != nil && amendments[i.Identity.SetupID] {
			return errors.New("identity_amendment repeats KyIdentity's setup_id")
		}
		// apply checks an installed app's client against the catalog.
		if i.IdentityClient != "" && (!i.Installed(s) || i.Product == "kyidentity") {
			if _, _, err := s.IdentitySource(i); err != nil {
				return err
			}
		}
	}
	return nil
}

// Installed reports whether i selects an app the plan installs on i's target.
func (i Integration) Installed(s *Stack) bool {
	for _, a := range s.Apps {
		if a.Name == i.Product && a.Target == i.Target {
			return true
		}
	}
	return false
}

// SetupID is the setup UUID naming an installed app's bundle directory,
// <data>/installer/<UUID>/setup.json, or "" for any other path.
func (i Integration) SetupID() string {
	dir := path.Dir(i.Bundle)
	id, err := uuid.Parse(path.Base(dir))
	if !preparationPath(i.Bundle) || path.Base(i.Bundle) != "setup.json" || path.Base(path.Dir(dir)) != "installer" || err != nil || id.String() != path.Base(dir) {
		return ""
	}
	return id.String()
}

// IdentitySource validates the selected source/destination graph before any connection.
// The canonical owner subject is resolved later from the source's native private export.
func (s *Stack) IdentitySource(destination Integration) (Integration, IdentityClient, error) {
	bad := errors.New("credential transfer requires a matching prepared KyIdentity client and KyNotes destination")
	if destination.Product != "kynotes" || destination.Notes == nil || destination.Notes.SSO == nil || len(destination.Notes.Admins) != 0 {
		return Integration{}, IdentityClient{}, bad
	}
	n := destination.Notes.SSO
	dir := path.Dir(destination.Bundle)
	if destination.IdentityClient != n.ClientID || n.ClientSecretFile != path.Join(dir, "client.secret") || n.DirectoryHMACSecretFile != "" && n.DirectoryHMACSecretFile != path.Join(dir, "directory.secret") {
		return Integration{}, IdentityClient{}, bad
	}
	for _, source := range s.Integrations {
		if source.Product != "kyidentity" || source.Identity == nil {
			continue
		}
		for _, client := range source.Identity.Clients {
			if client.ID != destination.IdentityClient {
				continue
			}
			callback := false
			for _, uri := range client.RedirectURIs {
				callback = callback || uri == n.RedirectURI
			}
			if !callback || client.AdministratorRole != "kynotes.admin" || client.EverydayRole != "kynotes.user" || client.SCIM != nil {
				return Integration{}, IdentityClient{}, bad
			}
			if n.DirectoryHMACSecretFile == "" {
				if client.SuiteWebhook != nil {
					return Integration{}, IdentityClient{}, bad
				}
			} else {
				u, err := url.Parse(n.RedirectURI)
				if err != nil {
					return Integration{}, IdentityClient{}, bad
				}
				if client.SuiteWebhook == nil || client.SuiteWebhook.URL != "https://"+u.Host+"/api/v1/sync/events" {
					return Integration{}, IdentityClient{}, bad
				}
			}
			for _, existing := range s.Existing {
				if existing.Product == "kyidentity" && existing.Target == source.Target && strings.TrimRight(existing.URL, "/") != n.IssuerURL {
					return Integration{}, IdentityClient{}, bad
				}
			}
			return source, client, nil
		}
	}
	return Integration{}, IdentityClient{}, bad
}
