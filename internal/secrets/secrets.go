package secrets

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/util"
)

const FileName = "secrets.json"

var ExportableKeys = map[string]bool{
	"libre_url":             true,
	"lingva_url":            true,
	"oneringtranslator_url": true,
	"deeplx_url":            true,
}

type SecretValue struct {
	ID     string `json:"id"`
	Value  string `json:"value"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

type SecretState struct {
	ID     string `json:"id"`
	Value  string `json:"value"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

type Manager struct {
	mu                sync.Mutex
	filePath          string
	backupsDir        string
	allowKeysExposure bool
}

func NewManager(userRoot, backupsDir string, allowKeysExposure bool) *Manager {
	return &Manager{
		filePath:          filepath.Join(userRoot, FileName),
		backupsDir:        backupsDir,
		allowKeysExposure: allowKeysExposure,
	}
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hex := func(v byte) string { return fmt.Sprintf("%02x", v) }
	s := ""
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			s += "-"
		}
		s += hex(v)
	}
	return s
}

type secretFileCache struct {
	modTime time.Time
	size    int64
	secrets map[string][]SecretValue
}

var (
	secretCacheMu sync.Mutex
	secretCache   = map[string]secretFileCache{}
)

func parseSecrets(data []byte) map[string][]SecretValue {
	out := make(map[string][]SecretValue)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for k, v := range raw {
		var arr []SecretValue
		if err := json.Unmarshal(v, &arr); err == nil {
			out[k] = arr
		}
	}
	return out
}

func readSecretsCached(path string) map[string][]SecretValue {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	secretCacheMu.Lock()
	defer secretCacheMu.Unlock()
	if c, ok := secretCache[path]; ok && c.modTime.Equal(st.ModTime()) && c.size == st.Size() {
		return c.secrets
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := parseSecrets(data)
	secretCache[path] = secretFileCache{modTime: st.ModTime(), size: st.Size(), secrets: out}
	return out
}

func invalidateSecretCache(path string) {
	secretCacheMu.Lock()
	delete(secretCache, path)
	secretCacheMu.Unlock()
}

func (m *Manager) readFile() map[string][]SecretValue {
	out := make(map[string][]SecretValue)
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		_ = util.AtomicWrite(m.filePath, []byte("{}"))
		return out
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for k, v := range raw {
		var arr []SecretValue
		if err := json.Unmarshal(v, &arr); err == nil {
			out[k] = arr
		}
	}
	return out
}

func (m *Manager) writeFile(secrets map[string][]SecretValue) {
	data, err := json.MarshalIndent(secrets, "", "    ")
	if err != nil {
		return
	}
	_ = util.AtomicWrite(m.filePath, data)
	invalidateSecretCache(m.filePath)
}

func (m *Manager) MaskedValue(value, key string) string {
	if m.allowKeysExposure || ExportableKeys[key] {
		return value
	}
	if len(value) <= 10 {
		return strings.Repeat("*", 10)
	}
	return strings.Repeat("*", 7) + value[len(value)-3:]
}

func (m *Manager) WriteSecret(key, value, label string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if label == "" {
		label = "Unlabeled"
	}
	secrets := m.readFile()
	arr := secrets[key]
	for i := range arr {
		arr[i].Active = false
	}
	id := newUUID()
	arr = append(arr, SecretValue{ID: id, Value: value, Label: label, Active: true})
	secrets[key] = arr
	m.writeFile(secrets)
	return id
}

func (m *Manager) DeleteSecret(key, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.filePath); err != nil {
		return
	}
	secrets := m.readFile()
	arr, ok := secrets[key]
	if !ok {
		return
	}
	idx := -1
	for i, sv := range arr {
		if (id != "" && sv.ID == id) || (id == "" && sv.Active) {
			idx = i
			break
		}
	}
	if idx != -1 {
		arr = append(arr[:idx], arr[idx+1:]...)
	}
	anyActive := false
	for _, sv := range arr {
		if sv.Active {
			anyActive = true
			break
		}
	}
	if len(arr) > 0 && !anyActive {
		arr[0].Active = true
	}
	if len(arr) == 0 {
		delete(secrets, key)
	} else {
		secrets[key] = arr
	}
	m.writeFile(secrets)
}

func (m *Manager) ReadSecret(key, id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.filePath); err != nil {
		return ""
	}
	arr, ok := m.readFile()[key]
	if !ok {
		return ""
	}
	for _, sv := range arr {
		if (id != "" && sv.ID == id) || (id == "" && sv.Active) {
			return sv.Value
		}
	}
	return ""
}

func (m *Manager) RotateSecret(key, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.filePath); err != nil {
		return
	}
	secrets := m.readFile()
	arr, ok := secrets[key]
	if !ok {
		return
	}
	found := false
	for i := range arr {
		if arr[i].ID == id {
			found = true
		}
		arr[i].Active = false
	}
	if !found {
		return
	}
	for i := range arr {
		if arr[i].ID == id {
			arr[i].Active = true
		}
	}
	secrets[key] = arr
	m.writeFile(secrets)
}

func (m *Manager) RenameSecret(key, id, label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	secrets := m.readFile()
	arr, ok := secrets[key]
	if !ok {
		return
	}
	for i := range arr {
		if arr[i].ID == id {
			arr[i].Label = label
		}
	}
	secrets[key] = arr
	m.writeFile(secrets)
}

func (m *Manager) GetSecretState(allKeys []string) map[string][]SecretState {
	m.mu.Lock()
	defer m.mu.Unlock()
	secrets := m.readFile()
	state := make(map[string][]SecretState, len(allKeys))
	for _, key := range allKeys {
		arr, ok := secrets[key]
		if !ok || len(arr) == 0 {
			state[key] = nil
			continue
		}
		list := make([]SecretState, 0, len(arr))
		for _, sv := range arr {
			list = append(list, SecretState{
				ID:     sv.ID,
				Value:  m.MaskedValue(sv.Value, key),
				Label:  sv.Label,
				Active: sv.Active,
			})
		}
		state[key] = list
	}
	return state
}

func (m *Manager) GetAllSecrets() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string)
	for key, arr := range m.readFile() {
		if key == "_migrated" {
			continue
		}
		for _, sv := range arr {
			if sv.Active {
				result[key] = sv.Value
				break
			}
		}
	}
	return result
}

func (m *Manager) MigrateFlatSecrets() {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if len(raw) == 0 {
		return
	}
	if _, ok := raw["_migrated"]; ok {
		return
	}
	for _, v := range raw {
		var arr []any
		if json.Unmarshal(v, &arr) == nil {
			return
		}
	}
	migrated := make(map[string][]SecretValue)
	for key, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			continue
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		migrated[key] = []SecretValue{{ID: newUUID(), Value: s, Label: key, Active: true}}
	}
	migrated["_migrated"] = []SecretValue{}
	if m.backupsDir != "" {
		_ = os.MkdirAll(m.backupsDir, 0o755)
		if src, err := os.ReadFile(m.filePath); err == nil {
			_ = util.AtomicWrite(filepath.Join(m.backupsDir, fmt.Sprintf("secrets_migration_%d.json", time.Now().UnixMilli())), src)
		}
	}
	out, err := json.MarshalIndent(migrated, "", "    ")
	if err != nil {
		return
	}
	_ = util.AtomicWrite(m.filePath, out)
	invalidateSecretCache(m.filePath)
}

func ReadActiveSecret(userRoot, key string) string {
	path := filepath.Join(userRoot, FileName)
	secrets := readSecretsCached(path)
	if secrets == nil {
		m := NewManager(userRoot, filepath.Join(userRoot, "backups"), false)
		m.MigrateFlatSecrets()
		v := m.ReadSecret(key, "")
		invalidateSecretCache(path)
		return v
	}
	if _, migrated := secrets["_migrated"]; !migrated {
		m := NewManager(userRoot, filepath.Join(userRoot, "backups"), false)
		m.MigrateFlatSecrets()
		invalidateSecretCache(path)
		secrets = readSecretsCached(path)
		if secrets == nil {
			return ""
		}
	}
	for _, sv := range secrets[key] {
		if sv.Active {
			return sv.Value
		}
	}
	return ""
}
