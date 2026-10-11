// Package offboard owns durable, target-local work for the resident bridge.
package offboard

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"
)

var ErrState = errors.New("bridge state is unavailable, unowned or conflicting")
var ErrPending = errors.New("product revocation remains pending")

// Binding is supplied by verified product provisioning, never by a SCIM request.
// Credentials live separately on the target and are not part of this record.
type Binding struct {
	SetupID    string `json:"setup_id"`
	App        string `json:"app"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin"`
	Issuer     string `json:"issuer"`
	Subject    string `json:"subject"`
	NativeID   int64  `json:"native_id"`
	NativeUUID string `json:"native_uuid,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SourceID   int64  `json:"source_id,omitempty"`
	Username   string `json:"username,omitempty"`
}
type bindingRecord struct {
	Schema   string  `json:"schema"`
	Binding  Binding `json:"binding"`
	Creation bool    `json:"creation,omitempty"`
}

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var setupID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var nativeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)
var nativeUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validClientID(value string) bool {
	return value != "" && len(value) <= 255 && strings.IndexFunc(value, func(r rune) bool { return r <= 32 || r >= 127 }) < 0
}

var recordID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func https(raw string, origin bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery &&
		!strings.Contains(raw, "#") && u.Opaque == "" && (!origin || u.Path == "") && len(raw) <= 2048 && utf8.ValidString(raw) &&
		!strings.Contains(raw, `\`) && strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
func validate(b Binding) error {
	if !setupID.MatchString(b.SetupID) || !appName.MatchString(b.App) || !https(b.Origin, true) || !https(b.Issuer, false) ||
		!validSubject(b.Subject) {
		return ErrState
	}
	switch b.Kind {
	case "bookstack":
		if b.NativeID <= 0 || b.NativeUUID != "" || b.ClientID != "" || b.SourceID != 0 || b.Username != "" {
			return ErrState
		}
	case "forgejo":
		if b.NativeID <= 0 || b.NativeUUID != "" || b.ClientID != "" || b.SourceID <= 0 || !nativeName.MatchString(b.Username) {
			return ErrState
		}
	case "immich":
		if b.NativeID != 0 || !nativeUUID.MatchString(b.NativeUUID) || !validClientID(b.ClientID) || b.SourceID != 0 || b.Username != "" || !validClientID(b.Subject) {
			return ErrState
		}
	default:
		return ErrState
	}
	return nil
}
func bindingID(b Binding) string {
	sum := sha256.Sum256([]byte(b.App + "\x00" + b.Issuer + "\x00" + b.Subject))
	return hex.EncodeToString(sum[:])
}

type Store struct {
	root     *os.Root
	lease    *os.File
	mu       sync.Mutex
	worker   sync.Mutex
	audit    sync.Mutex
	accounts map[string]*accountLocks
}
type accountLocks struct {
	state    sync.Mutex
	delivery sync.Mutex
}

// Open requires a provisioned private target directory. Only one resident writer
// may own it; individual accounts have separate files and separate request locks.
func Open(path string) (*Store, error) {
	root, err := openPrivateRoot(path)
	if err != nil {
		return nil, err
	}
	// Lock the directory inode itself so removing a lock-file pathname cannot
	// silently create a second independent writer lease.
	lease, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, ErrState
	}
	if syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lease.Close()
		root.Close()
		return nil, ErrState
	}
	return &Store{root: root, lease: lease, accounts: map[string]*accountLocks{}}, nil
}

// openPrivateRoot is shared by state and read-only runtime configuration.
func openPrivateRoot(path string) (*os.Root, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || real != path {
		return nil, ErrState
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() {
			return nil, ErrState
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) ||
			(st.Mode().Perm()&0022 != 0 && !(owner.Uid == 0 && st.Mode()&os.ModeSticky != 0)) {
			return nil, ErrState
		}
		if p == path && (st.Mode().Perm() != 0700 || owner.Uid != uint32(os.Geteuid())) {
			return nil, ErrState
		}
		if p == "/" {
			break
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrState
	}
	return root, nil
}

// Close runs after the service has stopped accepting work and joined its worker.
func (s *Store) Close() error { return errors.Join(s.lease.Close(), s.root.Close()) }
func private(f *os.File, limit int64) error {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > limit {
		return ErrState
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return ErrState
	}
	return nil
}
func (s *Store) account(id string) *accountLocks {
	s.mu.Lock()
	m := s.accounts[id]
	if m == nil {
		m = &accountLocks{}
		s.accounts[id] = m
	}
	s.mu.Unlock()
	return m
}
func (s *Store) lock(id string) func() {
	m := s.account(id)
	m.state.Lock()
	return m.state.Unlock
}
func (s *Store) read(name string, out any) error {
	raw, err := readPrivate(s.root, name, 16384)
	if err != nil {
		return err
	}
	if !uniqueJSON(raw) {
		return ErrState
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrState
	}
	return nil
}

func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	if checkPrivateRoot(root) != nil {
		return nil, ErrState
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	if private(f, limit) != nil {
		return nil, ErrState
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrState
	}
	return raw, nil
}
func (s *Store) write(name string, in any) error {
	return s.save(name, in, false)
}
func (s *Store) publish(name string, in any) error {
	return s.save(name, in, true)
}
func (s *Store) save(name string, in any, immutable bool) error {
	if s.privateRoot() != nil {
		return ErrState
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 16384 {
		return ErrState
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrState
	}
	tmp := "tmp-" + hex.EncodeToString(nonce[:])
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrState
	}
	defer s.root.Remove(tmp)
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return ErrState
	}
	if immutable {
		err = s.root.Link(tmp, name)
	} else {
		err = s.root.Rename(tmp, name)
	}
	if err != nil {
		return ErrState
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrState
	}
	return nil
}
func (s *Store) privateRoot() error {
	return checkPrivateRoot(s.root)
}
func checkPrivateRoot(root *os.Root) error {
	st, err := root.Stat(".")
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return ErrState
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return ErrState
	}
	return nil
}

// Bind is a local provisioning operation. The caller must verify exact native
// identity/ownership first. A changed retained binding is never overwritten.
func (s *Store) Bind(b Binding) (string, error) {
	return s.bind(b, false)
}

func (s *Store) bind(b Binding, creation bool) (string, error) {
	if validate(b) != nil {
		return "", ErrState
	}
	id := bindingID(b)
	unlock := s.lock(id)
	defer unlock()
	var stored bindingRecord
	err := s.read(id+".binding", &stored)
	if err == nil {
		if stored.Schema != "ky.offboarding-binding/1" || stored.Binding != b || (creation && !stored.Creation) {
			return "", ErrState
		}
		if stored.Creation {
			job, err := s.creationJob(id)
			if err != nil || (job.Status != "created" && job.Status != "complete") {
				return "", ErrState
			}
			receipt, err := s.creationReceipt(id, job)
			if err != nil || receipt != b {
				return "", ErrState
			}
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", ErrState
	}
	if err := s.publish(id+".binding", bindingRecord{Schema: "ky.offboarding-binding/1", Binding: b, Creation: creation}); err != nil {
		return "", err
	}
	return id, nil
}
func (s *Store) Binding(id string) (Binding, error) {
	var stored bindingRecord
	if !recordID.MatchString(id) || s.read(id+".binding", &stored) != nil || stored.Schema != "ky.offboarding-binding/1" || validate(stored.Binding) != nil || bindingID(stored.Binding) != id {
		return Binding{}, ErrState
	}
	if stored.Creation {
		job, err := s.creationJob(id)
		if err != nil || (job.Status != "created" && job.Status != "complete") {
			return Binding{}, ErrState
		}
		receipt, err := s.creationReceipt(id, job)
		if err != nil || receipt != stored.Binding {
			return Binding{}, ErrState
		}
	}
	return stored.Binding, nil
}

func validSubject(subject string) bool {
	return subject != "" && len(subject) <= 255 && utf8.ValidString(subject) && strings.IndexFunc(subject, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
