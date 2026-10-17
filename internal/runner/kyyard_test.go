package runner

import "time"

// KyYard's client types (KyYard-Server internal/suite/suite.go, PR #127), copied verbatim
// because that package cannot be imported. KyYard decodes every answer into these with
// unknown fields refused and no trailing data; the tests here do the same.

type kyError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type kyRunner struct {
	ReleaseSet   string `json:"release_set"`
	RunnerID     string `json:"runner_id"`
	PlanRevision int64  `json:"plan_revision"`
	PlanOwner    string `json:"plan_owner"`
}

type kyTarget struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type kyHealth struct {
	OK      bool      `json:"ok"`
	Checked time.Time `json:"checked"`
}

type kyResult struct {
	Step   string    `json:"step"`
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

type kyApp struct {
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	Version    string    `json:"version"`
	Target     string    `json:"target"`
	State      string    `json:"state"`
	Health     *kyHealth `json:"health"`
	LastResult *kyResult `json:"last_result"`
}

type kyStatus struct {
	Runner    kyRunner   `json:"runner"`
	Operation *string    `json:"operation"`
	Targets   []kyTarget `json:"targets"`
	Apps      []kyApp    `json:"apps"`
}

type kyUpgrade struct {
	MinFrom      string   `json:"min_from"`
	NoSkip       []string `json:"no_skip"`
	MigratesData bool     `json:"migrates_data"`
}

type kyCatalogApp struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Version     string    `json:"version"`
	DependsOn   []string  `json:"depends_on"`
	Installed   bool      `json:"installed"`
	Upgrade     kyUpgrade `json:"upgrade"`
}

type kyCatalog struct {
	ReleaseSet string         `json:"release_set"`
	Apps       []kyCatalogApp `json:"apps"`
}

type kyPlan struct {
	Revision int64  `json:"revision"`
	Stack    string `json:"stack"`
}

type kyRequest struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	Apps                []string `json:"apps"`
	Target              string   `json:"target,omitempty"`
	AcknowledgeNoBackup bool     `json:"acknowledge_no_backup"`
	Actor               string   `json:"actor"`
}

type kyStep struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Time        time.Time `json:"time"`
	Diagnostics string    `json:"diagnostics"`
}

type kyOperation struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Apps     []string   `json:"apps"`
	Actor    string     `json:"actor"`
	State    string     `json:"state"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished"`
	Steps    []kyStep   `json:"steps"`
	Error    *kyError   `json:"error"`
}

var (
	kyKinds  = []string{"install", "uninstall", "unmanage", "upgrade"}
	kyStates = []string{"running", "succeeded", "failed", "unknown"}
)
