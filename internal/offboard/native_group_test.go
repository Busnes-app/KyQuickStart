package offboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeForgejoGroupOwnership(t *testing.T) {
	if os.Getenv("KYQ_TEST_FORGEJO_GROUP_OWNERSHIP") != "1" {
		t.Skip("run pinned Forgejo fixture with KYQ_TEST_FORGEJO_TEAMS=1")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	var input struct {
		Container, Image, Origin, Issuer, Token string
		CA                                      []byte
		OrganizationID                          int64     `json:"organization_id"`
		SourceID                                int64     `json:"source_id"`
		RepositoryID                            int64     `json:"repository_id"`
		OrdinaryToken                           string    `json:"ordinary_token"`
		Bindings                                []Binding `json:"bindings"`
	}
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &input) != nil || !regexp.MustCompile(`^kyq-forgejo-api-[0-9]+$`).MatchString(input.Container) || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(input.Image) || !strings.HasPrefix(input.Origin, "https://127.0.0.1:") || input.OrganizationID <= 0 || input.SourceID <= 0 || input.RepositoryID <= 0 || len(input.Bindings) != 3 || input.OrdinaryToken == "" {
		t.Fatal("unowned native group fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configuration, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", input.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	if err != nil || json.Unmarshal(configuration, &config) != nil || config.Image != input.Image || config.Labels["kyq.fixture"] != "forgejo" || config.Labels["kyq.fixture-owner"] != input.Container {
		t.Fatal("native group fixture ownership unverified")
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(input.CA) {
		t.Fatal("native group fixture trust invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	scope := GroupBinding{SetupID: "271333e2-c221-4601-b249-563b3d828530", App: "forgejo", Origin: input.Origin, Issuer: input.Issuer, ExternalID: "native-owned-group", SourceID: input.SourceID, OrganizationID: input.OrganizationID, Organization: "fixture-org", TeamName: "bridge-owned-team", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	target, err := NewTarget(TargetSpec{SetupID: scope.SetupID, App: scope.App, Kind: "forgejo", Origin: scope.Origin, Issuer: scope.Issuer, SourceID: scope.SourceID}, input.Token)
	if err != nil {
		t.Fatal("native group target rejected")
	}
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	b, err := s.CreateGroup(ctx, target, scope, time.Now())
	if err != nil || b.TeamID <= 0 {
		t.Fatal("native group ownership failed", err)
	}
	id := groupID(scope)
	s.Close()
	s = openStore(t, path)
	repeated, err := s.CreateGroup(ctx, target, scope, time.Now())
	if err != nil || !sameGroup(b, repeated) {
		t.Fatal("native group repeat/restart changed binding", err)
	}
	retained, err := s.GroupBinding(id)
	if err != nil || !sameGroup(retained, b) {
		t.Fatal("native group receipt missing", err)
	}
	accounts := []string{}
	for _, account := range input.Bindings {
		if account.SetupID != scope.SetupID || !target.matches(account) {
			t.Fatal("native group account scope invalid")
		}
		accountID, err := s.Register(ctx, target, account)
		if err != nil {
			t.Fatal("native group account registration failed", err)
		}
		accounts = append(accounts, accountID)
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirect refused") }}
	api := func(method, path, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, input.Origin+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal("native group fixture request invalid")
		}
		req.Header.Set("Authorization", "token "+token)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("native group fixture request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		if err != nil || len(body) > 16384 {
			t.Fatal("native group fixture response invalid")
		}
		return response.StatusCode, body
	}
	base := "/teams/" + strconv.FormatInt(b.TeamID, 10)
	if status, _ := api(http.MethodPut, base+"/repos/fixture-org/shared-content", input.Token); status != 204 {
		t.Fatal("native fixture repository assignment failed")
	}
	probe := func(allowed bool) {
		t.Helper()
		status, body := api(http.MethodGet, "/repos/fixture-org/shared-content", input.OrdinaryToken)
		var repo struct {
			ID int64 `json:"id"`
		}
		if allowed {
			if status != 200 || json.Unmarshal(body, &repo) != nil || repo.ID != input.RepositoryID {
				t.Fatal("native bridge membership did not grant scoped access")
			}
		} else if status != 403 && status != 404 {
			t.Fatal("native bridge membership denial not proved")
		}
	}
	p := GroupMembership{Owned: []string{accounts[0], accounts[1]}, Members: []string{accounts[1]}}
	probe(false)
	admin := p
	admin.Members = []string{accounts[0]}
	if err := s.ApplyGroupMembership(ctx, target, id, admin, time.Now()); err == nil {
		t.Fatal("native administrator received ordinary group membership")
	}
	if err := s.ApplyGroupMembership(ctx, target, id, p, time.Now()); err != nil {
		t.Fatal("native bridge membership grant failed", err)
	}
	probe(true)
	if err := s.ApplyGroupMembership(ctx, target, id, p, time.Now()); err != nil {
		t.Fatal("native bridge membership repeat failed", err)
	}
	s.Close()
	s = openStore(t, path)
	if j, err := s.membershipJob(id); err != nil || j.Status != "complete" {
		t.Fatal("native membership proof lost across restart", err)
	}
	if status, _ := api(http.MethodPut, base+"/members/"+input.Bindings[2].Username, input.Token); status != 204 {
		t.Fatal("native foreign member fixture failed")
	}
	withdraw := p
	withdraw.Members = []string{}
	if err := s.ApplyGroupMembership(ctx, target, id, withdraw, time.Now()); err == nil {
		t.Fatal("native foreign member did not refuse replacement")
	}
	probe(true)
	if status, _ := api(http.MethodDelete, base+"/members/"+input.Bindings[2].Username, input.Token); status != 204 {
		t.Fatal("native foreign fixture cleanup failed")
	}
	if err := s.ApplyGroupMembership(ctx, target, id, withdraw, time.Now()); err != nil {
		t.Fatal("native bridge membership withdrawal failed", err)
	}
	probe(false)
	status, body := api(http.MethodGet, "/repos/fixture-org/shared-content", input.Token)
	var repo struct {
		ID int64 `json:"id"`
	}
	if status != 200 || json.Unmarshal(body, &repo) != nil || repo.ID != input.RepositoryID {
		t.Fatal("native group withdrawal lost content")
	}
	groupTarget, err := NewTarget(TargetSpec{SetupID: scope.SetupID, App: scope.App, Kind: "forgejo", Origin: scope.Origin, Issuer: scope.Issuer, SourceID: scope.SourceID, AdministratorRole: "admin", EverydayRole: "editor", Groups: &GroupConfig{OrganizationID: scope.OrganizationID, Organization: scope.Organization, Permission: "read", Permissions: map[string]string{"repo.code": "read"}}}, input.Token)
	if err != nil {
		t.Fatal("native SCIM group policy rejected", err)
	}
	receiver, err := NewReceiver(s, groupTarget, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	scim := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal("native SCIM fixture request invalid")
		}
		req.Header.Set("Authorization", "Bearer "+receiver.token)
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("native SCIM group request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		if err != nil || len(data) > 16384 {
			t.Fatal("native SCIM group response invalid")
		}
		return response.StatusCode, data
	}
	status, body = scim(http.MethodPost, receiver.base+"/Groups", groupBody("SCIM engineering", accounts[1]))
	var created struct {
		ID string `json:"id"`
	}
	if status != 201 || json.Unmarshal(body, &created) != nil || !recordID.MatchString(created.ID) {
		t.Fatal("native SCIM group creation failed", status)
	}
	managed, err := s.GroupBinding(created.ID)
	if err != nil {
		t.Fatal("native SCIM team receipt missing", err)
	}
	if status, _ := api(http.MethodPut, "/teams/"+strconv.FormatInt(managed.TeamID, 10)+"/repos/fixture-org/shared-content", input.Token); status != 204 {
		t.Fatal("native SCIM fixture repository assignment failed")
	}
	probe(true)
	groupPath := receiver.base + "/Groups/" + created.ID
	if status, _ := scim(http.MethodPut, groupPath, groupBody("Renamed SCIM engineering", accounts[1])); status != 204 {
		t.Fatal("native SCIM group rename failed", status)
	}
	if status, data := scim(http.MethodGet, groupPath, ""); status != 200 || !bytes.Contains(data, []byte("Renamed SCIM engineering")) {
		t.Fatal("native SCIM group label read failed", status)
	}
	if status, _ := scim(http.MethodDelete, groupPath, ""); status != 204 {
		t.Fatal("native SCIM group retirement failed", status)
	}
	probe(false)
	if status, _ := scim(http.MethodGet, groupPath, ""); status != 404 {
		t.Fatal("native SCIM group still active", status)
	}
	if status, _ := scim(http.MethodPut, groupPath, groupBody("Reactivated", accounts[1])); status != 409 {
		t.Fatal("native SCIM group resurrected", status)
	}
	if status, _ := api(http.MethodGet, "/repos/fixture-org/shared-content", input.Token); status != 200 {
		t.Fatal("native SCIM retirement lost repository")
	}
	if _, err := s.Request(accounts[1], time.Now()); err != nil {
		t.Fatal("native membership revocation fixture failed")
	}
	if err := s.ApplyGroupMembership(ctx, target, id, p, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatal("native group membership bypassed account revocation intent", err)
	}
	foreign := scope
	foreign.ExternalID = "unowned-different-group"
	if _, err := s.CreateGroup(ctx, target, foreign, time.Now()); err == nil {
		t.Fatal("native existing team adopted by another group")
	}
	if _, err := s.GroupBinding(groupID(foreign)); err == nil {
		t.Fatal("foreign group gained a native binding")
	}
	j, _, err := s.groupCreation(id)
	if err != nil || s.root.Remove(groupAuditName(id, j.Attempt, "result")) != nil {
		t.Fatal("native receipt fixture removal failed")
	}
	if _, err := s.GroupBinding(id); err == nil {
		t.Fatal("missing native ownership proof accepted")
	}
	if _, err := s.CreateGroup(ctx, target, scope, time.Now()); !errors.Is(err, ErrState) {
		t.Fatal("missing proof authorized native recreation", err)
	}
	t.Log("native group receipts/membership and TLS SCIM create/rename/read/retirement, restart/repeat, administrator/foreign-member/foreign-name refusal, withdrawal, revocation fencing, repository retention and missing-proof refusal verified")
}
