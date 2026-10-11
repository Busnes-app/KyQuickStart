package offboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeDirectoryMappingIsExplicitAndValidatedBeforeCredentials(t *testing.T) {
	for _, mode := range []string{"valid", "create", "immich", "immich-create", "bookstack", "missing-role", "same-role", "invalid-role"} {
		t.Run(mode, func(t *testing.T) {
			file, cfg, _, mutations := runtimeFixture(t)
			cfg.Apps[0].Kind, cfg.Apps[0].SourceID = "forgejo", 7
			cfg.Apps[0].Directory = &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "user"}
			switch mode {
			case "immich", "immich-create":
				cfg.Apps[0].Kind, cfg.Apps[0].ClientID, cfg.Apps[0].SourceID = "immich", "photos-client", 0
				cfg.Apps[0].Directory.CreateAccounts = mode == "immich-create"
			case "create":
				cfg.Apps[0].Directory.CreateAccounts = true
			case "bookstack":
				cfg.Apps[0].Kind, cfg.Apps[0].SourceID = "bookstack", 0
			case "missing-role":
				cfg.Apps[0].Directory.EverydayRole = ""
			case "same-role":
				cfg.Apps[0].Directory.EverydayRole = "admin"
			case "invalid-role":
				cfg.Apps[0].Directory.AdministratorRole = "private\nvalue"
			}
			writeRuntime(t, file, cfg)
			loaded, err := LoadRuntime(file)
			if mode == "valid" || mode == "create" || mode == "immich" || mode == "immich-create" {
				if err != nil || loaded.apps[0].target.directory == nil || loaded.apps[0].target.directoryRead == nil {
					t.Fatal("explicit native mapping not enabled", err)
				}
				if (loaded.apps[0].target.create != nil) != (mode == "create" || mode == "immich-create") {
					t.Fatal("account creation was not explicitly scoped")
				}
			} else if !errors.Is(err, ErrRuntime) || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid directory mapping accepted or leaked", err)
			}
			if mutations.Load() != 0 {
				t.Fatal("runtime loading mutated a product")
			}
		})
	}
}

func TestBookStackRuntimeRequiresSeparateAuthorityAndOwnedAccountMap(t *testing.T) {
	for _, mode := range []string{"valid", "missing-map", "unsorted-roles", "shared-file", "shared-token", "unsafe-file", "creation", "wrong-admin"} {
		t.Run(mode, func(t *testing.T) {
			file, cfg, _, mutations := runtimeFixture(t)
			app := &cfg.Apps[0]
			app.Directory = &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "editor", TokenFile: "directory-token", BookStack: &BookStackDirectoryConfig{ClientID: "books-client", OrdinaryRoleIDs: []int64{2}, Accounts: map[string]BookStackAccount{"subject": {ID: 2, SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f"}}}}
			token := strings.Repeat("ef", 32)
			switch mode {
			case "missing-map":
				app.Directory.BookStack = nil
			case "unsorted-roles":
				app.Directory.BookStack.OrdinaryRoleIDs = []int64{3, 2}
			case "shared-file":
				app.Directory.TokenFile = app.TokenFile
			case "shared-token":
				token = strings.Repeat("cd", 32)
			case "creation":
				app.Directory.CreateAccounts = true
			case "wrong-admin":
				app.Directory.AdministratorRole = "manager"
			}
			path := filepath.Join(filepath.Dir(file), "directory-token")
			if err := os.WriteFile(path, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "unsafe-file" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			writeRuntime(t, file, cfg)
			runtime, err := LoadRuntime(file)
			if mode == "valid" {
				if err != nil || runtime.apps[0].target.directory == nil || runtime.apps[0].target.directoryRead == nil || runtime.apps[0].target.create != nil {
					t.Fatal("registered-account directory mode unavailable", err)
				}
			} else if !errors.Is(err, ErrRuntime) {
				t.Fatal("invalid BookStack directory authority accepted", err)
			}
			if mutations.Load() != 0 {
				t.Fatal("runtime load contacted a native product")
			}
		})
	}
}
