package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// recomputeAll rebuilds the character index and then refreshes group recency.
// The rebuild re-derives each character's last-used time from its own chats as it
// goes (ProcessCharacter -> ChatStats), so one instrumented walk covers both the
// index and the dates.
func (h *CharacterHandler) recomputeAll(dirs models.UserDirectories) error {
	h.recompute.setPhase("characters", 0)
	h.Index.RebuildIndexWithProgress(dirs.Characters, character.ProcessCharacter, dirs,
		func(done, total int) { h.recompute.setProgress(done, total) })
	return h.recomputeGroups(dirs)
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
			for _, chatID := range gd.Chats {
				chatPath := filepath.Join(dirs.GroupChats, chatID+".jsonl")
				if ts := character.ChatFileRecency(chatPath); ts > newest {
					newest = ts
				}
			}
			if newest > 0 && newest != gd.DateLastChat {
				gd.DateLastChat = newest
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
