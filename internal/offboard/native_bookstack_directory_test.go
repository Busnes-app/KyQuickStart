package offboard

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeBookStackDirectory(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_HTTP_CONTAINER")
	if container == "" {
		t.Skip("run disposable BookStack fixture with KYQ_TEST_BOOKSTACK_DIRECTORY=1")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[a-f0-9]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("unowned directory fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatal("owned directory fixture operation failed")
		}
		return out
	}
	var config struct {
		Image  string
		Labels map[string]string
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if json.Unmarshal(docker("inspect", "--format", "{{json .Config}}", container), &config) != nil || config.Image != image ||
		!regexp.MustCompile(`^\S+@sha256:[a-f0-9]{64}$`).MatchString(image) || config.Labels["kyq.fixture"] != "bookstack" || config.Labels["kyq.fixture-owner"] != owner {
		t.Fatal("directory fixture ownership/image unverified")
	}
	port := strings.TrimSpace(string(docker("port", "kyq-bookstack-"+owner+"-gateway", "8443")))
	if !strings.HasPrefix(port, "127.0.0.1:") || strings.Contains(port, "\n") {
		t.Fatal("directory fixture is not loopback-only")
	}
	credential := func(name string) string {
		var value struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(docker("exec", "-u", "abc", container, "cat", "/config/kyquickstart-bridge/"+name), &value) != nil {
			t.Fatal("directory fixture credential unavailable")
		}
		return value.Token
	}
	b := fixtureBinding()
	b.Origin, b.Issuer, b.Subject = "https://"+port, "https://kyq-bookstack-"+owner+"-gateway:8443/fixture-idp", os.Getenv("KYQ_TEST_BOOKSTACK_DAILY_SUBJECT")
	var err error
	b.NativeID, err = strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_USER_ID"), 10, 64)
	var roles []int64
	if err != nil || validate(b) != nil || json.Unmarshal([]byte(os.Getenv("KYQ_TEST_BOOKSTACK_ORDINARY_ROLE_IDS")), &roles) != nil {
		t.Fatal("native directory binding invalid")
	}
	admin := b
	admin.Subject = os.Getenv("KYQ_TEST_BOOKSTACK_OWNER_SUBJECT")
	admin.NativeID, err = strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_OWNER_NATIVE_ID"), 10, 64)
	var ownerRoles []int64
	if err != nil || validate(admin) != nil || admin.NativeID == b.NativeID || admin.Subject == b.Subject || json.Unmarshal([]byte(os.Getenv("KYQ_TEST_BOOKSTACK_OWNER_ORDINARY_ROLE_IDS")), &ownerRoles) != nil {
		t.Fatal("native owner scope invalid")
	}
	runNativeDirectory(t, nativeDirectoryFixture{Container: container, Image: image, Token: credential("token.json"), DirectoryToken: credential("directory.json"),
		CA: docker("exec", container, "cat", "/fixture/ca.pem"), Binding: b, AdditionalBindings: []Binding{admin},
		BookStack: &BookStackDirectoryConfig{ClientID: "fixture-client", OrdinaryRoleIDs: roles, Accounts: map[string]BookStackAccount{b.Subject: {ID: b.NativeID, SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f"}, admin.Subject: {ID: admin.NativeID, SetupID: "2c676879-9c71-4913-b8a7-995521d9ed20", OrdinaryRoleIDs: ownerRoles}}}})
}
