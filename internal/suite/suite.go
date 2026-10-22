// Package suite holds the suite runner's wire types: error codes and status. The CLI
// returns them and serve encodes them as they are.
package suite

import (
	"errors"
	"fmt"
	"time"
)

// Code is a machine-readable refusal, as listed in the suite runner design.
type Code string

const (
	InvalidRequest   Code = "invalid_request"
	IDConflict       Code = "id_conflict"
	Busy             Code = "busy"
	UnknownApp       Code = "unknown_app"
	UnknownTarget    Code = "unknown_target"
	AlreadyInstalled Code = "already_installed"
	NotInstalled     Code = "not_installed"
	Dependency       Code = "dependency"
	PlanStale        Code = "plan_stale"
	TargetLocked     Code = "target_locked"
	UpgradeRefused   Code = "upgrade_refused"
	BackupRequired   Code = "backup_required"
	AckRequired      Code = "ack_required"
	IssuerChanged    Code = "issuer_changed" // the plan moves a URL a completed KyIdentity setup is bound to
	RunnerHost       Code = "runner_host"    // the runner never changes the app whose pod it runs in
	// KyMessages: every Matrix ID names the server name; KyIdentity's chat redirect the provider ID.
	ServerNameChanged  Code = "server_name_changed"
	ProviderIDMismatch Code = "provider_id_mismatch"
	// Backup and restore (kyquickstart backup/restore, Docker apps).
	BackupUnconfigured Code = "backup_unconfigured" // no backup block, or its target has no destination
	BackupUnsupported  Code = "backup_unsupported"  // a backup volume is a named Docker volume
	RestoreRefused     Code = "restore_refused"     // nothing was changed
	RestoreIncomplete  Code = "restore_incomplete"  // restored and running, but the removal replay did not deliver
	AppStopped         Code = "app_stopped"         // stopped with kyquickstart stop; start it first
	StepFailed         Code = "step_failed"
	OwnedByRunner      Code = "owned_by_runner"     // the CLI may not change a plan the runner owns
	OwnedByCLI         Code = "owned_by_cli"        // the runner acts only on a plan handed to it
	OwnedByOtherPlan   Code = "owned_by_other_plan" // the target's marker names another installation_id
	NotFound           Code = "not_found"           // runner protocol: no such operation or route
	Internal           Code = "internal"            // runner protocol: any failure without a code above
)

type Error struct {
	Code    Code
	Message string
	Err     error // the cause, if any
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error; a %w verb becomes its cause.
func Errorf(code Code, format string, a ...any) *Error {
	err := fmt.Errorf(format, a...)
	return &Error{Code: code, Message: err.Error(), Err: errors.Unwrap(err)}
}

// Status is the body of GET /v1/status.
type Status struct {
	Runner    Runner   `json:"runner"`
	Operation *string  `json:"operation"`
	Targets   []Target `json:"targets"`
	Apps      []App    `json:"apps"`
}

type Runner struct {
	ReleaseSet   string `json:"release_set"`
	RunnerID     string `json:"runner_id"`
	PlanRevision int    `json:"plan_revision"`
	PlanOwner    string `json:"plan_owner"`
}

type Target struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // docker or kubernetes
}

type App struct {
	Name       string  `json:"name"`
	Category   string  `json:"category"`
	Version    string  `json:"version"` // deployed version; empty when unknown
	Target     string  `json:"target"`
	State      string  `json:"state"` // installed, failed or unknown
	Health     *Health `json:"health"`
	LastResult *Result `json:"last_result"`
	// The CLI's alone (KyYard decodes the runner's status strictly): the live workload,
	// running, stopped, missing or unknown, and whether kyquickstart stop stopped the app.
	Workload string `json:"workload,omitempty"`
	Stopped  bool   `json:"stopped,omitempty"`
}

type Health struct {
	OK      bool      `json:"ok"`
	Checked time.Time `json:"checked"`
}

type Result struct {
	Step   string    `json:"step"`
	Status string    `json:"status"` // ok or failed
	Time   time.Time `json:"time"`
}

// Catalog is the body of GET /v1/catalog.
type Catalog struct {
	ReleaseSet string       `json:"release_set"`
	Apps       []CatalogApp `json:"apps"`
}

type CatalogApp struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Version     string   `json:"version"`
	DependsOn   []string `json:"depends_on"`
	Installed   bool     `json:"installed"`
	Upgrade     Upgrade  `json:"upgrade"`
}

type Upgrade struct {
	MinFrom      string   `json:"min_from"`
	NoSkip       []string `json:"no_skip"`
	MigratesData bool     `json:"migrates_data"`
}

// Request is the body of POST /v1/operations.
type Request struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	Apps                []string `json:"apps"`
	Target              string   `json:"target,omitempty"`
	AcknowledgeNoBackup bool     `json:"acknowledge_no_backup"`
	Actor               string   `json:"actor"`
}

// Operation is the body of GET /v1/operations/{id}.
type Operation struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Apps     []string   `json:"apps"`
	Actor    string     `json:"actor"`
	State    string     `json:"state"` // running, succeeded, failed or unknown
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished"`
	Steps    []Step     `json:"steps"`
	Error    *Refusal   `json:"error"`
}

type Step struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"` // running, ok, failed or skipped
	Time        time.Time `json:"time"`
	Diagnostics string    `json:"diagnostics"`
}

// Refusal is an error on the wire: a refused request's body is {"error": Refusal}.
type Refusal struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}
