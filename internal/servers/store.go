package servers

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/ktav-lang/golang/pure"
)

const storeFileName = "server-secrets.ktav"

var storeMu sync.Mutex
var secretFieldRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Store securely persists generated protocol material and user secrets.
type Store struct{ path string }

type storeEntry struct {
	Material map[string]string            `json:"material"`
	Users    map[string]map[string]string `json:"users"`
}

// NewStore roots the store at home/server-secrets.ktav.
func NewStore(home string) *Store { return &Store{path: filepath.Join(home, storeFileName)} }

// Path returns the secrets file path.
func (s *Store) Path() string { return s.path }

// String and GoString redact the store path and contents.
func (s *Store) String() string   { return "<redacted>" }
func (s *Store) GoString() string { return "<redacted>" }

func validSecretKey(k string) bool   { return secretFieldRE.MatchString(k) }
func validSecretName(n string) bool  { return serverNameRE.MatchString(n) }
func validSecretLogin(n string) bool { return accountLoginRE.MatchString(n) }

func (s *Store) read() (map[string]storeEntry, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]storeEntry{}, nil
	}
	if err != nil {
		return nil, errors.New("cannot read server secrets")
	}
	var shape map[string]any
	if _, err := pure.LoadsStrict(string(b)); err != nil {
		return nil, errors.New("invalid server secrets file")
	}
	if err := pure.LoadsInto(string(b), &shape); err != nil || len(shape) != 1 {
		return nil, errors.New("invalid server secrets file")
	}
	serversValue, ok := shape["servers"]
	if !ok {
		return nil, errors.New("invalid server secrets file")
	}
	if _, ok := serversValue.(map[string]any); !ok {
		return nil, errors.New("invalid server secrets file")
	}
	var doc struct {
		Servers map[string]storeEntry `json:"servers"`
	}
	if err := pure.LoadsInto(string(b), &doc); err != nil {
		return nil, errors.New("invalid server secrets file")
	}
	if doc.Servers == nil {
		doc.Servers = map[string]storeEntry{}
	}
	for n, e := range doc.Servers {
		if !validSecretName(n) {
			return nil, errors.New("invalid server secrets file")
		}
		for k := range e.Material {
			if !validSecretKey(k) {
				return nil, errors.New("invalid server secrets file")
			}
		}
		for login, fields := range e.Users {
			if !validSecretLogin(login) {
				return nil, errors.New("invalid server secrets file")
			}
			for k := range fields {
				if !validSecretKey(k) {
					return nil, errors.New("invalid server secrets file")
				}
			}
		}
	}
	return doc.Servers, nil
}

// Load validates the on-disk secrets document without exposing parse details.
func (s *Store) Load() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err := s.read()
	return err
}
func (s *Store) write(entries map[string]storeEntry) error {
	text, err := pure.Dumps(struct {
		Servers map[string]storeEntry `json:"servers"`
	}{entries})
	if err != nil {
		return errors.New("cannot encode server secrets")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("cannot write server secrets")
	}
	tmp, err := os.CreateTemp(dir, ".server-secrets-*.tmp")
	if err != nil {
		return errors.New("cannot write server secrets")
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.New("cannot write server secrets")
	}
	if _, err := tmp.WriteString(text); err != nil {
		return errors.New("cannot write server secrets")
	}
	if err := tmp.Sync(); err != nil {
		return errors.New("cannot write server secrets")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("cannot write server secrets")
	}
	if err := os.Rename(name, s.path); err != nil {
		return errors.New("cannot write server secrets")
	}
	ok = true
	return nil
}
func cloneEntry(e storeEntry) storeEntry {
	n := storeEntry{Material: cloneMap(e.Material), Users: make(map[string]map[string]string, len(e.Users))}
	for k, v := range e.Users {
		n.Users[k] = cloneMap(v)
	}
	return n
}
func mergeMissing(dst map[string]string, src map[string]string) bool {
	changed := false
	for k, v := range src {
		old, ok := dst[k]
		if !ok || old == "" {
			dst[k] = v
			changed = true
		}
	}
	return changed
}
func validateGenerated(m map[string]string) error {
	for k, v := range m {
		if !validSecretKey(k) || v == "" {
			return errors.New("invalid generated secret")
		}
	}
	return nil
}

// EnsureSecrets fills missing generated fields and persists only when changed.
func (s *Store) EnsureSecrets(spec Spec) (Spec, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := spec.Validate(); err != nil {
		return spec, errors.New("invalid server specification")
	}
	if !validSecretName(spec.Name) {
		return spec, errors.New("invalid server name")
	}
	p, ok := Lookup(spec.Protocol)
	if !ok {
		return spec, errors.New("unknown server protocol")
	}
	for _, login := range spec.Accounts {
		if !validSecretLogin(login) {
			return spec, errors.New("invalid server login")
		}
	}
	entries, err := s.read()
	if err != nil {
		return spec, err
	}
	e, exists := entries[spec.Name]
	if !exists {
		e = storeEntry{Material: map[string]string{}, Users: map[string]map[string]string{}}
	} else {
		e = cloneEntry(e)
	}
	changed := false
	if e.Material == nil {
		e.Material = map[string]string{}
	}
	if e.Users == nil {
		e.Users = map[string]map[string]string{}
	}
	gen, err := p.NewMaterial(spec.Params)
	if err != nil {
		return spec, errors.New("cannot generate server material")
	}
	if err = validateGenerated(gen); err != nil {
		return spec, err
	}
	if mergeMissing(e.Material, gen) {
		changed = true
	}
	for _, login := range spec.Accounts {
		fields, ok := e.Users[login]
		if !ok || fields == nil {
			fields = map[string]string{}
			e.Users[login] = fields
		}
		gen, err := p.NewSecret(login, spec.Params)
		if err != nil {
			return spec, errors.New("cannot generate server secret")
		}
		if err = validateGenerated(gen); err != nil {
			return spec, err
		}
		if mergeMissing(fields, gen) {
			changed = true
		}
	}
	if !exists {
		changed = true
	}
	if changed {
		entries[spec.Name] = e
		if err = s.write(entries); err != nil {
			return spec, err
		}
	}
	spec.Material = SecretMap(cloneMap(e.Material))
	spec.Users = UserSecrets(e.Users)
	return spec, nil
}

// Apply returns the configured entry's stored secrets without generating any.
func (s *Store) Apply(spec Spec) (Spec, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if !validSecretName(spec.Name) {
		return spec, errors.New("invalid server name")
	}
	entries, err := s.read()
	if err != nil {
		return spec, err
	}
	e, ok := entries[spec.Name]
	if !ok {
		return spec, errors.New("server secrets not found")
	}
	for _, login := range spec.Accounts {
		if !validSecretLogin(login) {
			return spec, errors.New("invalid server login")
		}
		if _, ok := e.Users[login]; !ok {
			return spec, errors.New("server secret not found")
		}
	}
	spec.Material = SecretMap(cloneMap(e.Material))
	spec.Users = UserSecrets(e.Users)
	return spec, nil
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Rotate replaces one account's secret, or material and all configured accounts when login is empty.
func (s *Store) Rotate(spec Spec, login string) (Spec, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if !validSecretName(spec.Name) {
		return spec, errors.New("invalid server name")
	}
	p, ok := Lookup(spec.Protocol)
	if !ok {
		return spec, errors.New("unknown server protocol")
	}
	entries, err := s.read()
	if err != nil {
		return spec, err
	}
	e, ok := entries[spec.Name]
	if !ok {
		return spec, errors.New("server secrets not found")
	}
	e = cloneEntry(e)
	if login != "" {
		if !validSecretLogin(login) || !containsString(spec.Accounts, login) {
			return spec, errors.New("invalid server login")
		}
		if _, ok := e.Users[login]; !ok {
			return spec, errors.New("server secret not found")
		}
		m, err := p.NewSecret(login, spec.Params)
		if err != nil {
			return spec, errors.New("cannot generate server secret")
		}
		if err = validateGenerated(m); err != nil {
			return spec, err
		}
		e.Users[login] = m
	} else {
		m, err := p.NewMaterial(spec.Params)
		if err != nil {
			return spec, errors.New("cannot generate server material")
		}
		if err = validateGenerated(m); err != nil {
			return spec, err
		}
		e.Material = m
		for _, u := range spec.Accounts {
			if !validSecretLogin(u) {
				return spec, errors.New("invalid server login")
			}
			m, err := p.NewSecret(u, spec.Params)
			if err != nil {
				return spec, errors.New("cannot generate server secret")
			}
			if err = validateGenerated(m); err != nil {
				return spec, err
			}
			e.Users[u] = m
		}
	}
	entries[spec.Name] = e
	if err = s.write(entries); err != nil {
		return spec, err
	}
	spec.Material = SecretMap(cloneMap(e.Material))
	spec.Users = UserSecrets(e.Users)
	return spec, nil
}

// Remove deletes a named entry; removing an absent entry is a no-op.
func (s *Store) Remove(name string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if !validSecretName(name) {
		return errors.New("invalid server name")
	}
	e, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := e[name]; !ok {
		return nil
	}
	delete(e, name)
	return s.write(e)
}

// Prune removes entries not represented in specs.
func (s *Store) Prune(specs []Spec) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	e, err := s.read()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, v := range specs {
		if !validSecretName(v.Name) {
			return errors.New("invalid server name")
		}
		keep[v.Name] = true
	}
	changed := false
	for n := range e {
		if !keep[n] {
			delete(e, n)
			changed = true
		}
	}
	if changed {
		return s.write(e)
	}
	return nil
}

// Names returns stored server names in sorted order. Read errors return an empty slice.
func (s *Store) Names() []string {
	storeMu.Lock()
	defer storeMu.Unlock()
	e, err := s.read()
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(e))
	for n := range e {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
