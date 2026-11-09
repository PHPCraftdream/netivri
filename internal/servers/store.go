package servers

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/PHPCraftdream/netivri/internal/cli"
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

func mergeMaterial(dst, generated, params map[string]string) ([]CreatedSecretField, bool) {
	var receipt []CreatedSecretField
	changed := false
	set := func(key, value string) {
		old, present := dst[key]
		if present && old == value {
			return
		}
		receipt = append(receipt, CreatedSecretField{Key: key, Value: value, Previous: old, HadPrevious: present})
		dst[key] = value
		changed = true
	}
	remove := func(key string) {
		old, present := dst[key]
		if !present {
			return
		}
		receipt = append(receipt, CreatedSecretField{Key: key, Previous: old, HadPrevious: true, Removed: true})
		delete(dst, key)
		changed = true
	}

	forced := map[string]bool{}
	configuredFiles := params[tlsCertFileKey] != "" && params[tlsPrivateKeyFileKey] != ""
	if configuredFiles && generated[tlsCertFileKey] != "" && generated[tlsPrivateKeyFileKey] != "" {
		for _, key := range []string{tlsCertFileKey, tlsPrivateKeyFileKey, tlsFingerprintKey} {
			if value, ok := generated[key]; ok {
				set(key, value)
				forced[key] = true
			}
		}
		remove(tlsCertKey)
		remove(tlsKeyKey)
		forced[tlsCertKey], forced[tlsKeyKey] = true, true
	} else if generated[tlsCertKey] != "" && generated[tlsKeyKey] != "" && (dst[tlsCertKey] == "" || dst[tlsKeyKey] == "") {
		for _, key := range []string{tlsCertKey, tlsKeyKey, tlsFingerprintKey} {
			if value, ok := generated[key]; ok {
				set(key, value)
				forced[key] = true
			}
		}
	}
	for key, value := range generated {
		if forced[key] {
			continue
		}
		old, present := dst[key]
		if !present || old == "" {
			set(key, value)
		}
	}
	return receipt, changed
}

func applyConfiguredTLS(spec Spec, material map[string]string) (map[string]string, error) {
	_, hasCert := spec.Params[tlsCertFileKey]
	_, hasKey := spec.Params[tlsPrivateKeyFileKey]
	if !hasCert && !hasKey {
		return material, nil
	}
	configured, err := NewTLSMaterial(spec.Params, "")
	if err != nil {
		return nil, errors.New("cannot apply server TLS material")
	}
	for _, key := range []string{tlsCertFileKey, tlsPrivateKeyFileKey, tlsFingerprintKey} {
		if value, ok := configured[key]; ok {
			material[key] = value
		}
	}
	delete(material, tlsCertKey)
	delete(material, tlsKeyKey)
	return material, nil
}

func validateGenerated(m map[string]string) error {
	for k, v := range m {
		if !validSecretKey(k) || v == "" {
			return errors.New("invalid generated secret")
		}
	}
	return nil
}

// CreatedSecretField records one field changed by EnsureSecretsTracked.
type CreatedSecretField struct {
	Key         string
	Value       string
	Previous    string
	HadPrevious bool
	Removed     bool
}

func (CreatedSecretField) String() string   { return "<redacted>" }
func (CreatedSecretField) GoString() string { return "<redacted>" }

// EnsureSecretsReceipt identifies fields changed by one ensure operation.
type EnsureSecretsReceipt struct {
	Server       string
	CreatedEntry bool
	Material     []CreatedSecretField
	Users        map[string][]CreatedSecretField
	CreatedUsers []string

	expectedTLS tlsMaterialState
	hasTLSState bool
}

type tlsFieldState struct {
	value   string
	present bool
}

type tlsMaterialState struct {
	certPEM     tlsFieldState
	keyPEM      tlsFieldState
	certFile    tlsFieldState
	privateFile tlsFieldState
	fingerprint tlsFieldState
}

func captureTLSMaterialState(material map[string]string) tlsMaterialState {
	field := func(key string) tlsFieldState {
		value, present := material[key]
		return tlsFieldState{value: value, present: present}
	}
	return tlsMaterialState{
		certPEM:     field(tlsCertKey),
		keyPEM:      field(tlsKeyKey),
		certFile:    field(tlsCertFileKey),
		privateFile: field(tlsPrivateKeyFileKey),
		fingerprint: field(tlsFingerprintKey),
	}
}

func isTLSMaterialKey(key string) bool {
	switch key {
	case tlsCertKey, tlsKeyKey, tlsCertFileKey, tlsPrivateKeyFileKey, tlsFingerprintKey:
		return true
	default:
		return false
	}
}

func (EnsureSecretsReceipt) String() string   { return "<redacted>" }
func (EnsureSecretsReceipt) GoString() string { return "<redacted>" }

// EnsureSecrets fills missing generated fields and persists only when changed.
func (s *Store) EnsureSecrets(spec Spec) (Spec, error) {
	ensured, _, err := s.EnsureSecretsTracked(spec)
	return ensured, err
}

// EnsureSecretsTracked fills missing fields and returns a receipt for newly added fields.
func (s *Store) EnsureSecretsTracked(spec Spec) (Spec, EnsureSecretsReceipt, error) {
	return s.ensureSecrets(spec, nil, nil)
}

// EnsureSecretsValidated ensures secrets only after validating the target with current peers.
func (s *Store) EnsureSecretsValidated(spec Spec, peers []Spec) (Spec, EnsureSecretsReceipt, error) {
	return s.ensureSecrets(spec, peers, func(target Spec, validated []Spec) error {
		if err := ValidateSharedPort(validated); err != nil {
			return err
		}
		if err := ValidateTrialSecrets(validated); err != nil {
			return &cli.Error{Code: "SERVER_PORT_AMBIGUOUS", Message: "shared endpoint credentials are ambiguous", Exit: cli.ExitConflict}
		}
		return nil
	})
}

func (s *Store) ensureSecrets(spec Spec, peers []Spec, validate func(Spec, []Spec) error) (Spec, EnsureSecretsReceipt, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	receipt := EnsureSecretsReceipt{Server: spec.Name, Users: map[string][]CreatedSecretField{}}
	unlock, err := s.lock()
	if err != nil {
		return spec, receipt, err
	}
	defer unlock()
	if err := spec.Validate(); err != nil {
		return spec, receipt, errors.New("invalid server specification")
	}
	if !validSecretName(spec.Name) {
		return spec, receipt, errors.New("invalid server name")
	}
	p, ok := Lookup(spec.Protocol)
	if !ok {
		return spec, receipt, errors.New("unknown server protocol")
	}
	for _, login := range spec.Accounts {
		if !validSecretLogin(login) {
			return spec, receipt, errors.New("invalid server login")
		}
	}
	entries, err := s.read()
	if err != nil {
		return spec, receipt, err
	}
	validatedPeers := make([]Spec, 0, len(peers))
	for _, peer := range peers {
		entry, ok := entries[peer.Name]
		if !ok {
			return spec, receipt, errors.New("server secrets not found")
		}
		for _, login := range peer.Accounts {
			if _, ok := entry.Users[login]; !ok {
				return spec, receipt, errors.New("server secret not found")
			}
		}
		peer.Material = SecretMap(cloneMap(entry.Material))
		peerMaterial, err := applyConfiguredTLS(peer, map[string]string(peer.Material))
		if err != nil {
			return spec, receipt, err
		}
		peer.Material = SecretMap(peerMaterial)
		peer.Users = UserSecrets(entry.Users)
		validatedPeers = append(validatedPeers, peer)
	}
	e, exists := entries[spec.Name]
	receipt.CreatedEntry = !exists
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
		return spec, EnsureSecretsReceipt{}, errors.New("cannot generate server material")
	}
	if err = validateGenerated(gen); err != nil {
		return spec, EnsureSecretsReceipt{}, err
	}
	fields, materialChanged := mergeMaterial(e.Material, gen, spec.Params)
	receipt.Material = append(receipt.Material, fields...)
	if materialChanged {
		changed = true
	}
	for _, login := range spec.Accounts {
		fields, ok := e.Users[login]
		if !ok || fields == nil {
			receipt.CreatedUsers = append(receipt.CreatedUsers, login)
			fields = map[string]string{}
			e.Users[login] = fields
		}
		gen, err := p.NewSecret(login, spec.Params)
		if err != nil {
			return spec, EnsureSecretsReceipt{}, errors.New("cannot generate server secret")
		}
		if err = validateGenerated(gen); err != nil {
			return spec, EnsureSecretsReceipt{}, err
		}
		for k, v := range gen {
			old, present := fields[k]
			if !present || old == "" {
				receipt.Users[login] = append(receipt.Users[login], CreatedSecretField{Key: k, Value: v, Previous: old, HadPrevious: present})
			}
		}
		if mergeMissing(fields, gen) {
			changed = true
		}
	}
	if !exists {
		changed = true
	}
	if validate != nil {
		validatedTarget := spec
		validatedTarget.Material = SecretMap(cloneMap(e.Material))
		validatedTarget.Users = UserSecrets(e.Users)
		validated := append(validatedPeers, validatedTarget)
		if err := validate(validatedTarget, validated); err != nil {
			return spec, EnsureSecretsReceipt{}, err
		}
	}
	if changed {
		entries[spec.Name] = e
		if err = s.write(entries); err != nil {
			return spec, EnsureSecretsReceipt{}, err
		}
	}
	receipt.expectedTLS = captureTLSMaterialState(e.Material)
	receipt.hasTLSState = true
	spec.Material = SecretMap(cloneMap(e.Material))
	spec.Users = UserSecrets(e.Users)
	return spec, receipt, nil
}

// CleanupCreated removes unchanged fields created by a tracked ensure operation.
func (s *Store) CleanupCreated(receipt EnsureSecretsReceipt) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if !receipt.CreatedEntry && len(receipt.Material) == 0 && len(receipt.Users) == 0 && len(receipt.CreatedUsers) == 0 {
		return nil
	}
	entries, err := s.read()
	if err != nil {
		return err
	}
	e, ok := entries[receipt.Server]
	if !ok {
		return nil
	}
	e = cloneEntry(e)
	changed := false
	hasTLSReceipt := false
	for _, field := range receipt.Material {
		if isTLSMaterialKey(field.Key) {
			hasTLSReceipt = true
			break
		}
	}
	restoreTLSGroup := hasTLSReceipt && receipt.hasTLSState && captureTLSMaterialState(e.Material) == receipt.expectedTLS
	for _, field := range receipt.Material {
		if isTLSMaterialKey(field.Key) && !restoreTLSGroup {
			continue
		}
		current, present := e.Material[field.Key]
		if field.Removed {
			if present {
				continue
			}
			e.Material[field.Key] = field.Previous
			changed = true
		} else if present && current == field.Value {
			if field.HadPrevious {
				e.Material[field.Key] = field.Previous
			} else {
				delete(e.Material, field.Key)
			}
			changed = true
		}
	}
	for login, fields := range receipt.Users {
		values, ok := e.Users[login]
		if !ok {
			continue
		}
		for _, field := range fields {
			if current, ok := values[field.Key]; ok && current == field.Value {
				if field.HadPrevious {
					values[field.Key] = field.Previous
				} else {
					delete(values, field.Key)
				}
				changed = true
			}
		}
	}
	for _, login := range receipt.CreatedUsers {
		if values, ok := e.Users[login]; ok && len(values) == 0 {
			delete(e.Users, login)
			changed = true
		}
	}
	if receipt.CreatedEntry && len(e.Material) == 0 && len(e.Users) == 0 {
		delete(entries, receipt.Server)
		changed = true
	} else if changed {
		entries[receipt.Server] = e
	}
	if !changed {
		return nil
	}
	return s.write(entries)
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
	material, err := applyConfiguredTLS(spec, map[string]string(spec.Material))
	if err != nil {
		return spec, err
	}
	spec.Material = SecretMap(material)
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
	unlock, err := s.lock()
	if err != nil {
		return spec, err
	}
	defer unlock()
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
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
