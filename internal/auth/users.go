package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/scrypt"
)

const (
	DefaultHandle   = "default-user"
	userKeyPrefix   = "user:"
	avatarKeyPrefix = "avatar:"
	publicAvatar    = "/img/default-user.png"
	settingsFile    = "settings.json"
	secretsFile     = "secrets.json"
)

type User struct {
	Handle   string `json:"handle"`
	Name     string `json:"name"`
	Created  int64  `json:"created"`
	Password string `json:"password"`
	Salt     string `json:"salt"`
	Admin    bool   `json:"admin"`
	Enabled  bool   `json:"enabled"`
}

type persistEnvelope struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type UserStore struct {
	dir string
	mu  sync.RWMutex
}

func NewUserStore(dataRoot string) *UserStore {
	return &UserStore{dir: filepath.Join(dataRoot, "_storage")}
}

func storageFileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *UserStore) pathFor(key string) string {
	return filepath.Join(s.dir, storageFileName(key))
}

func (s *UserStore) readEnvelope(key string, out any) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.pathFor(key))
	if err != nil {
		return false
	}
	var env persistEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return false
	}
	raw, err := json.Marshal(env.Value)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

func (s *UserStore) writeEnvelope(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	env := persistEnvelope{Key: key, Value: value}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.pathFor(key))
}

func (s *UserStore) GetUser(handle string) (*User, bool) {
	var u User
	if !s.readEnvelope(userKeyPrefix+handle, &u) {
		return nil, false
	}
	return &u, true
}

func (s *UserStore) SaveUser(u *User) error {
	return s.writeEnvelope(userKeyPrefix+u.Handle, u)
}

func (s *UserStore) DeleteUser(handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.pathFor(userKeyPrefix + handle))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *UserStore) ListUsers() []*User {
	s.mu.RLock()
	entries, err := os.ReadDir(s.dir)
	s.mu.RUnlock()
	if err != nil {
		return nil
	}
	var users []*User
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var env persistEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if !strings.HasPrefix(env.Key, userKeyPrefix) {
			continue
		}
		raw, err := json.Marshal(env.Value)
		if err != nil {
			continue
		}
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			continue
		}
		users = append(users, &u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Created < users[j].Created })
	return users
}

func (s *UserStore) GetAvatar(handle string) string {
	var avatar string
	if s.readEnvelope(avatarKeyPrefix+handle, &avatar) {
		return avatar
	}
	return ""
}

func (s *UserStore) SetAvatar(handle, avatar string) error {
	return s.writeEnvelope(avatarKeyPrefix+handle, avatar)
}

func NewSalt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func HashPassword(password, salt string) string {
	dk, err := scrypt.Key([]byte(password), []byte(salt), 16384, 8, 1, 64)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(dk)
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type RateLimiter struct {
	points int
	window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

func NewRateLimiter(points int, window time.Duration) *RateLimiter {
	return &RateLimiter{points: points, window: window, hits: make(map[string][]time.Time)}
}

func (l *RateLimiter) Consume(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.points {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	for k, ts := range l.hits {
		if k == ip {
			continue
		}
		allExpired := true
		for _, t := range ts {
			if t.After(cutoff) {
				allExpired = false
				break
			}
		}
		if allExpired {
			delete(l.hits, k)
		}
	}
	return true
}

func (l *RateLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, ip)
}

type codeEntry struct {
	code    string
	expires time.Time
}

type CodeCache struct {
	ttl time.Duration
	mu  sync.Mutex
	set map[string]codeEntry
}

func NewCodeCache(ttl time.Duration) *CodeCache {
	return &CodeCache{ttl: ttl, set: make(map[string]codeEntry)}
}

func (c *CodeCache) Set(key, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.set {
		if now.After(e.expires) {
			delete(c.set, k)
		}
	}
	c.set[key] = codeEntry{code: code, expires: now.Add(c.ttl)}
}

func (c *CodeCache) Get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.set[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.set, key)
		return ""
	}
	return e.code
}

func (c *CodeCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.set, key)
}

func NewRecoveryCode() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	n := int(b[0])<<8 | int(b[1])
	return fmt.Sprintf("%04d", 1000+n%9000)
}

var userDirTemplate = []string{
	"",
	"thumbnails",
	"thumbnails/bg",
	"thumbnails/avatar",
	"thumbnails/persona",
	"worlds",
	"user",
	"User Avatars",
	"user/images",
	"groups",
	"group chats",
	"chats",
	"characters",
	"backgrounds",
	"NovelAI Settings",
	"KoboldAI Settings",
	"OpenAI Settings",
	"TextGen Settings",
	"themes",
	"movingUI",
	"extensions",
	"instruct",
	"context",
	"QuickReplies",
	"assets",
	"user/workflows",
	"user/files",
	"vectors",
	"backups",
	"sysprompt",
	"reasoning",
}

func UserRoot(dataRoot, handle string) string {
	return filepath.Join(dataRoot, handle)
}

func EnsureUserDirs(dataRoot, handle string) models.UserDirectories {
	root := UserRoot(dataRoot, handle)
	join := func(elem ...string) string {
		return filepath.Join(append([]string{root}, elem...)...)
	}
	for _, rel := range userDirTemplate {
		_ = os.MkdirAll(join(rel), 0o755)
	}
	return models.UserDirectories{
		Root:        root,
		Characters:  join("characters"),
		Chats:       join("chats"),
		Groups:      join("groups"),
		GroupChats:  join("group chats"),
		Backups:     join("backups"),
		Thumbnails:  join("thumbnails"),
		Worlds:      join("worlds"),
		UserImages:  join("user/images"),
		User:        join("user"),
		Avatars:     join("User Avatars"),
		Backgrounds: join("backgrounds"),
		Uploads:     filepath.Join(dataRoot, "_uploads"),
		Assets:      join("assets"),
		Extensions:  join("extensions"),
		Files:       join("user/files"),
	}
}

var mimeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".svg":  "image/svg+xml",
}

func ResolveAvatar(store *UserStore, dirs models.UserDirectories, handle string) string {
	if custom := store.GetAvatar(handle); custom != "" {
		return custom
	}
	settingsPath := filepath.Join(dirs.Root, settingsFile)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return publicAvatar
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return publicAvatar
	}
	var avatarFile string
	if pu, ok := settings["power_user"].(map[string]any); ok {
		if v, ok := pu["default_persona"].(string); ok {
			avatarFile = v
		}
	}
	if avatarFile == "" {
		if v, ok := settings["user_avatar"].(string); ok {
			avatarFile = v
		}
	}
	if avatarFile == "" {
		return publicAvatar
	}
	avatarFile = SanitizeFileName(avatarFile)
	avatarPath := filepath.Join(dirs.Avatars, avatarFile)
	content, err := os.ReadFile(avatarPath)
	if err != nil {
		return publicAvatar
	}
	mime := mimeByExt[strings.ToLower(filepath.Ext(avatarPath))]
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(content)
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func SanitizeFileName(name string) string {
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if name == "" {
		return "unnamed"
	}
	return name
}

var deburrReplacer = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y", "ñ", "n", "ç", "c",
	"š", "s", "ž", "z", "ß", "ss", "æ", "ae", "œ", "oe",
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)
var edgeHyphens = regexp.MustCompile(`^-+|-+$`)

func Slugify(text string) string {
	s := deburrReplacer.Replace(strings.ToLower(strings.TrimSpace(text)))
	s = nonSlugChars.ReplaceAllString(s, "-")
	return edgeHyphens.ReplaceAllString(s, "")
}
