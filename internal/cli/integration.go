package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func integrationOrder(st *stack.Stack) ([]stack.Integration, error) {
	if len(st.Integrations) == 0 {
		return nil, errors.New("no product integrations selected in stack.yaml")
	}
	for _, i := range st.Integrations {
		if _, ok := productAdapters[i.Product]; !ok {
			return nil, fmt.Errorf("product %s has no supported setup adapter", i.Product)
		}
		if target, ok := st.Target(i.Target); ok {
			if err := validateProductTarget(i.Product, target); err != nil {
				return nil, err
			}
		}
	}
	ordered := append([]stack.Integration(nil), st.Integrations...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Product == "kyidentity" && ordered[j].Product != "kyidentity" })
	return ordered, nil
}

func integrateStack(ctx context.Context, args []string, o Options) error {
	f := flag.NewFlagSet("integrate-stack", flag.ContinueOnError)
	f.SetOutput(o.Out)
	state := f.String("state", ".", "state directory holding stack.yaml")
	check := f.Bool("check", false, "inspect all selected integrations without mutation")
	trust := trustFlag{}
	f.Var(trust, "trust-host-key", "accept an unknown host key")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return ErrUsage
	}
	st, err := stack.Load(filepath.Join(*state, "stack.yaml"))
	if err != nil {
		return err
	}
	ordered, err := integrationOrder(st)
	if err != nil {
		return err
	}
	// Validate the entire saved deployment before the first selected mutation.
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	s.close()
	if o.In != nil {
		o.In = bufio.NewReader(o.In)
	}
	// apply configures installed apps; this command is for existing products.
	ordered = slices.DeleteFunc(ordered, func(i stack.Integration) bool { return i.Installed(st) })
	if len(ordered) == 0 {
		return errors.New("every selected integration is an installed app's, which apply configures")
	}
	for _, i := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		args := []string{"--state", *state, "--product", i.Product, "--target", i.Target, "--container", i.Container, "--bundle", i.Bundle}
		if i.Namespace != "" {
			args = append(args, "--namespace", i.Namespace, "--pod", i.Pod)
		}
		if *check {
			args = append(args, "--check")
		}
		if i.Notes != nil || i.Identity != nil {
			args = append(args, "--prepare")
		}
		names := make([]string, 0, len(trust))
		for name := range trust {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			args = append(args, "--trust-host-key", name+"="+trust[name])
		}
		if err := integrateProductResult(ctx, args, o, "", func(report []byte) error {
			return json.NewEncoder(o.Out).Encode(struct {
				Product string          `json:"product"`
				Target  string          `json:"target"`
				Report  json.RawMessage `json:"report"`
			}{i.Product, i.Target, json.RawMessage(report)})
		}); err != nil {
			return fmt.Errorf("integration %s failed (previously reported products retain their changes): %w", i.Product, err)
		}
	}
	return nil
}

func guidedIntegration(p *prompts, st *stack.Stack, e stack.Existing) error {
	key := "integration." + e.Product + "." + e.Target + "."
	if _, ok := productAdapters[e.Product]; !ok {
		return nil
	}
	// Demo planning does not opt into configuring real products by default.
	if st.Demo && p.answers[key+"configure"] == "" {
		return nil
	}
	choice, err := p.ask(key+"configure", "Select product setup for "+e.Product+"? yes/no (integrate-stack changes its configuration)", "no", oneOf("yes", "no"))
	if err != nil || choice == "no" {
		return err
	}
	i := stack.Integration{Product: e.Product, Target: e.Target}
	t, _ := st.Target(e.Target)
	if err := validateProductTarget(e.Product, t); err != nil {
		return err
	}
	validContainer := workloadName.MatchString
	if t.Kubernetes() {
		validContainer = func(s string) bool {
			return kubeWorkloadName.MatchString(s) && !strings.Contains(s, ".") && len(s) <= 63
		}
	}
	i.Container, err = p.ask(key+"container", "Product container name", "", validContainer)
	if err != nil {
		return err
	}
	if t.Kubernetes() {
		i.Namespace, err = p.ask(key+"namespace", "Product Kubernetes namespace", "", func(s string) bool {
			return kubeWorkloadName.MatchString(s) && !strings.Contains(s, ".") && len(s) <= 63
		})
		if err != nil {
			return err
		}
		i.Pod, err = p.ask(key+"pod", "Product Kubernetes pod name", "", func(s string) bool { return kubeWorkloadName.MatchString(s) && len(s) <= 253 })
		if err != nil {
			return err
		}
	}
	i.Bundle, err = p.ask(key+"bundle", "Protected setup bundle path inside the container (contents stay on target)", "", func(s string) bool {
		return path.IsAbs(s) && path.Clean(s) == s && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n")
	})
	if err != nil {
		return err
	}
	if raw := p.answers[key+"notes"]; raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&i.Notes); err != nil || dec.Decode(new(any)) != io.EOF {
			return errors.New("invalid saved KyNotes preparation metadata")
		}
	}
	i.IdentityClient = p.answers[key+"identity-client"]
	if i.Notes != nil && i.Notes.SSO != nil {
		choice, err := p.ask(key+"identity-client", "KyIdentity client to transfer from selected setup (client ID, or none for existing target credentials)", "none", func(s string) bool { return s == "none" || s == i.Notes.SSO.ClientID })
		if err != nil {
			return err
		}
		i.IdentityClient = choice
		if choice == "none" {
			i.IdentityClient = ""
		}
	}
	if raw := p.answers[key+"identity"]; raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&i.Identity); err != nil || dec.Decode(new(any)) != io.EOF {
			return errors.New("invalid saved KyIdentity preparation metadata")
		}
	}
	st.Integrations = append(st.Integrations, i)
	return nil
}
