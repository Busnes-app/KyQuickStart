package catalog

import (
	"errors"
	"maps"

	"go.yaml.in/yaml/v3"
)

// WithDockerRunner shares only its socket with KyYard. Both use the target user's UID;
// the runner keeps credentials in its own private bind and has no Docker socket.
func (app App) WithDockerRunner(image string) (App, error) {
	if app.Kubernetes == nil || !app.Kubernetes.SuiteRunner || !Pinned(image) {
		return app, errors.New("Docker suite runner requires an owning app and a digest-pinned image")
	}
	var compose map[string]any
	if err := yaml.Unmarshal(app.Compose, &compose); err != nil {
		return app, err
	}
	services, ok := compose["services"].(map[string]any)
	if !ok {
		return app, errors.New("suite runner requires Compose services")
	}
	yard, ok := services["app"].(map[string]any)
	if !ok {
		return app, errors.New("suite runner requires the managed app service")
	}
	volumes, ok := yard["volumes"].([]any)
	if !ok {
		return app, errors.New("suite runner requires app volumes")
	}
	yard["volumes"] = append(volumes, "./runner-socket:/run/kyquickstart")
	services["runner"] = map[string]any{
		"image": image, "platform": "linux/amd64", "user": "${KYQ_UID:?}:${KYQ_GID:?}",
		"entrypoint": []string{"kyquickstart", "serve", "--state", "/state", "--socket", "/run/kyquickstart/runner.sock"},
		"restart":    "unless-stopped", "read_only": true, "cap_drop": []string{"ALL"},
		"security_opt": []string{"no-new-privileges:true"}, "tmpfs": []string{"/tmp"},
		"mem_limit": "512m", "memswap_limit": "512m", "cpus": 1, "pids_limit": 128,
		"stop_grace_period": "5m", "environment": map[string]string{"GOMEMLIMIT": "384MiB"},
		"volumes": []string{"./runner-state:/state", "./runner-socket:/run/kyquickstart"},
	}
	var err error
	app.Compose, err = yaml.Marshal(compose)
	if err != nil {
		return app, err
	}
	app.Images = maps.Clone(app.Images)
	app.Images["suite_runner"] = image
	app.Services = append(append([]string{}, app.Services...), "runner")
	app.Binds = append(append([]string{}, app.Binds...), "runner-state", "runner-socket")
	return app, nil
}
