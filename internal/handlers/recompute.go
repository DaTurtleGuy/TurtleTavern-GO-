package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

// recomputeProgress tracks a "recompute recent" pass so the UI can drive a
// determinate loading bar. Mirrors restoreProgress in userdata.go.
type recomputeProgress struct {
	mu      sync.Mutex
	running bool
	phase   string
	done    int
	total   int
	err     string
}

func (p *recomputeProgress) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return false
	}
	p.running = true
	p.phase = "characters"
	p.done = 0
	p.total = 0
	p.err = ""
	return true
}

func (p *recomputeProgress) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.phase = "idle"
	if err != nil {
		p.err = err.Error()
	}
}

func (p *recomputeProgress) setPhase(phase string, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.total = total
	p.done = 0
}

func (p *recomputeProgress) setProgress(done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = done
	p.total = total
}

func (p *recomputeProgress) tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
}

func (p *recomputeProgress) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{
		"running": p.running,
		"phase":   p.phase,
		"done":    p.done,
		"total":   p.total,
		"error":   p.err,
	}
}

func (h *CharacterHandler) RecomputeRecent(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !h.recompute.begin() {
		writeJSON(w, map[string]any{"started": false})
		return
	}
	dirs := uc.Directories
	go func() {
		h.recompute.finish(h.recomputeAll(dirs))
	}()
	writeJSON(w, map[string]any{"started": true})
}

func (h *CharacterHandler) RecomputeStatus(w http.ResponseWriter, r *http.Request) {
	if getUserCtx(r) == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	writeJSON(w, h.recompute.snapshot())
}

// recomputeAll repairs creation dates, rebuilds the character index and then
// refreshes group recency. Creation dates are fixed first so the rebuild reads
// the corrected values from date_added.json.
func (h *CharacterHandler) recomputeAll(dirs models.UserDirectories) error {
	h.recompute.setPhase("dates", 0)
	h.clampCreationDates(dirs)

	h.recompute.setPhase("characters", 0)
	h.Index.RebuildIndexWithProgress(dirs.Characters, character.ProcessCharacter, dirs,
		func(done, total int) { h.recompute.setProgress(done, total) })

	return h.recomputeGroups(dirs)
}

type dateAddedMap map[string]float64

func readDateAdded(path string) dateAddedMap {
	m := dateAddedMap{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// clampCreationDates lowers any creation date that postdates the character's own
// oldest message. That combination cannot be real: it only appears when a card
// was imported or restored, and it is what makes "Newest/Oldest" useless
// afterwards. The oldest message is the earliest evidence the character existed,
// so it becomes the creation date, in the card and in date_added.json alike.
func (h *CharacterHandler) clampCreationDates(dirs models.UserDirectories) {
	entries, err := os.ReadDir(dirs.Characters)
	if err != nil {
		return
	}
	var avatars []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			avatars = append(avatars, e.Name())
		}
	}
	h.recompute.setProgress(0, len(avatars))

	dateAddedFile := filepath.Join(dirs.Characters, "date_added.json")
	dateAdded := readDateAdded(dateAddedFile)
	dirty := false

	for i, avatar := range avatars {
		name := strings.TrimSuffix(avatar, ".png")
		oldest, ok := character.ChatOldestSendDate(filepath.Join(dirs.Chats, name))
		if ok {
			if cardDate, err := cardCreateDate(filepath.Join(dirs.Characters, avatar)); err == nil &&
				cardDate > 0 && cardDate > oldest {
				setCardCreateDate(filepath.Join(dirs.Characters, avatar), oldest)
			}
			if added, ok := dateAdded[name]; !ok || added > oldest {
				if !ok || added != oldest {
					dateAdded[name] = oldest
					dirty = true
				}
			}
		}
		h.recompute.setProgress(i+1, len(avatars))
	}

	if dirty {
		if data, err := json.MarshalIndent(dateAdded, "", "    "); err == nil {
			_ = util.AtomicWrite(dateAddedFile, data)
		}
	}
}

func cardCreateDate(path string) (float64, error) {
	raw, err := character.ReadCharacterDataFromFile(path)
	if err != nil {
		return 0, err
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return 0, err
	}
	value, _ := card["create_date"].(string)
	if value == "" {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, err
	}
	return float64(parsed.UnixMilli()), nil
}

func setCardCreateDate(path string, ts float64) {
	img, err := os.ReadFile(path)
	if err != nil {
		return
	}
	raw, err := character.ReadCharacterDataFromFile(path)
	if err != nil {
		return
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return
	}
	card["create_date"] = time.UnixMilli(int64(ts)).UTC().Format("2006-01-02T15:04:05.000Z")
	out, err := json.Marshal(card)
	if err != nil {
		return
	}
	png, err := character.WriteCharacterDataToPNG(img, string(out))
	if err != nil {
		return
	}
	_ = util.AtomicWrite(path, png)
}

// Group recency cannot be derived from anything cached per request without
// stat-ing every chat, so the derived value is written back into the group file
// the listing already reads.
func (h *CharacterHandler) recomputeGroups(dirs models.UserDirectories) error {
	files, err := os.ReadDir(dirs.Groups)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var groupFiles []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
			groupFiles = append(groupFiles, f.Name())
		}
	}
	h.recompute.setPhase("groups", len(groupFiles))
	for _, name := range groupFiles {
		path := filepath.Join(dirs.Groups, name)
		var gd models.GroupData
		if err := util.ReadJSONFile(path, &gd); err == nil {
			var newest float64
			var oldest float64
			for _, chatID := range gd.Chats {
				chatPath := filepath.Join(dirs.GroupChats, chatID+".jsonl")
				if ts := character.ChatFileRecency(chatPath); ts > newest {
					newest = ts
				}
				if ts, ok := character.ChatFileFirstSendDate(chatPath); ok && (oldest == 0 || ts < oldest) {
					oldest = ts
				}
			}
			changed := false
			if newest > 0 && newest != gd.DateLastChat {
				gd.DateLastChat = newest
				changed = true
			}
			// Same rule as characters: a group cannot predate its own oldest message.
			if oldest > 0 && (gd.DateAdded == 0 || gd.DateAdded > oldest) {
				gd.DateAdded = oldest
				gd.CreateDate = time.UnixMilli(int64(oldest)).UTC().Format(time.RFC3339)
				changed = true
			}
			if changed {
				util.WriteJSONFile(path, &gd)
			}
		}
		h.recompute.tick()
	}
	return nil
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
