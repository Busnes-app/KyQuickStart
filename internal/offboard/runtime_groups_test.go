package offboard

import (
	"testing"
)

func TestRuntimeGroupPolicyIsExplicitReadOnlyAndNonAdministrative(t *testing.T) {
	for _, mode := range []string{"valid", "bookstack", "missing-directory", "admin-permission", "invalid-unit", "missing-org"} {
		t.Run(mode, func(t *testing.T) {
			file, cfg, _, mutations := runtimeFixture(t)
			cfg.Apps[0].Kind, cfg.Apps[0].SourceID = "forgejo", 7
			cfg.Apps[0].Directory = &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "user"}
			cfg.Apps[0].Groups = &GroupConfig{OrganizationID: 12, Organization: "owned-org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
			switch mode {
			case "bookstack":
				cfg.Apps[0].Kind, cfg.Apps[0].SourceID = "bookstack", 0
			case "missing-directory":
				cfg.Apps[0].Directory = nil
			case "admin-permission":
				cfg.Apps[0].Groups.Permissions["repo.code"] = "admin"
			case "invalid-unit":
				cfg.Apps[0].Groups.Permissions["root"] = "write"
			case "missing-org":
				cfg.Apps[0].Groups.OrganizationID = 0
			}
			writeRuntime(t, file, cfg)
			loaded, err := LoadRuntime(file)
			if mode == "valid" {
				if err != nil || loaded.apps[0].target.spec.Groups == nil {
					t.Fatal("protected group policy rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid group policy accepted")
			}
			if mutations.Load() != 0 {
				t.Fatal("configuration loading mutated native state")
			}
		})
	}
}
