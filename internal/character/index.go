package character

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

const indexVersion = 1

type Index struct {
	db *sql.DB
	mu sync.Mutex
}

func OpenIndex(dbDir string) (*Index, error) {
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	dbPath := filepath.Join(dbDir, "character-index.db")
	db, err := sql.Open(sqliteDriver, dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	idx := &Index{db: db}
	if err := idx.initTables(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (idx *Index) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.db.Close()
}

func (idx *Index) initTables() error {
	ddl := `
	CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value INTEGER
	);
	CREATE TABLE IF NOT EXISTS characters (
		user_folder TEXT NOT NULL,
		avatar TEXT NOT NULL,
		name TEXT,
		fav INTEGER DEFAULT 0,
		date_added REAL DEFAULT 0,
		create_date REAL DEFAULT 0,
		date_last_chat REAL DEFAULT 0,
		chat_size INTEGER DEFAULT 0,
		data_size INTEGER DEFAULT 0,
		tags TEXT DEFAULT '[]',
		chat TEXT,
		creator TEXT,
		creator_notes TEXT,
		character_version TEXT,
		mtime REAL DEFAULT 0,
		PRIMARY KEY (user_folder, avatar)
	);
	CREATE INDEX IF NOT EXISTS idx_characters_user_folder ON characters(user_folder);
	CREATE INDEX IF NOT EXISTS idx_characters_name ON characters(name);
	CREATE INDEX IF NOT EXISTS idx_characters_fav ON characters(fav);
	CREATE INDEX IF NOT EXISTS idx_characters_date_last_chat ON characters(date_last_chat);
	`
	if _, err := idx.db.Exec(ddl); err != nil {
		return fmt.Errorf("init tables: %w", err)
	}
	var cnt int
	idx.db.QueryRow("SELECT COUNT(*) FROM meta WHERE key='version'").Scan(&cnt)
	if cnt == 0 {
		idx.db.Exec("INSERT INTO meta (key, value) VALUES ('version', ?)", indexVersion)
	}
	return nil
}

func (idx *Index) NeedsRebuild(userFolder string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.needsRebuild(userFolder)
}

func (idx *Index) needsRebuild(userFolder string) bool {
	var maxMtime sql.NullFloat64
	idx.db.QueryRow("SELECT MAX(mtime) FROM characters WHERE user_folder=?", userFolder).Scan(&maxMtime)
	indexedMtime := 0.0
	if maxMtime.Valid {
		indexedMtime = maxMtime.Float64
	}
	entries, err := os.ReadDir(userFolder)
	if err != nil {
		return true
	}
	pngCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			pngCount++
			info, err := e.Info()
			if err != nil {
				continue
			}
			if float64(info.ModTime().UnixMilli()) > indexedMtime {
				return true
			}
		}
	}
	var indexedCount int
	idx.db.QueryRow("SELECT COUNT(*) FROM characters WHERE user_folder=?", userFolder).Scan(&indexedCount)
	return indexedCount != pngCount
}

func (idx *Index) GetAllCharacters(userFolder string) []models.ShallowCharacter {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.getAllCharacters(userFolder)
}

func (idx *Index) getAllCharacters(userFolder string) []models.ShallowCharacter {
	rows, err := idx.db.Query(`
		SELECT avatar, name, fav, date_added, create_date, date_last_chat, chat_size, data_size, tags, chat, creator, creator_notes, character_version
		FROM characters WHERE user_folder=?`, userFolder)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []models.ShallowCharacter
	for rows.Next() {
		var sc models.ShallowCharacter
		var fav int
		var tagsJSON string
		err := rows.Scan(
			&sc.Avatar, &sc.Name, &fav, &sc.DateAdded, &sc.CreateDate,
			&sc.DateLastChat, &sc.ChatSize, &sc.DataSize, &tagsJSON,
			&sc.Chat, &sc.Data.Creator, &sc.Data.CreatorNotes, &sc.Data.CharacterVersion,
		)
		if err != nil {
			continue
		}
		sc.Shallow = true
		sc.Fav = fav != 0
		json.Unmarshal([]byte(tagsJSON), &sc.Tags)
		if sc.Tags == nil {
			sc.Tags = []string{}
		}
		sc.Data.Name = sc.Name
		sc.Data.Tags = sc.Tags
		sc.Data.Extensions.Fav = sc.Fav
		result = append(result, sc)
	}
	return result
}

// dbExecer is satisfied by both *sql.DB and *sql.Tx, so the rebuild can run
// its upserts inside one transaction while single upserts keep using the DB.
type dbExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (idx *Index) UpsertCharacter(userFolder string, char models.ShallowCharacter) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.upsertCharacter(userFolder, char)
}

func (idx *Index) upsertCharacter(userFolder string, char models.ShallowCharacter) {
	idx.upsertCharacterDB(idx.db, userFolder, char)
}

func (idx *Index) upsertCharacterDB(db dbExecer, userFolder string, char models.ShallowCharacter) {
	mtime := 0.0
	p := filepath.Join(userFolder, char.Avatar)
	if info, err := os.Stat(p); err == nil {
		mtime = float64(info.ModTime().UnixMilli())
	}
	tagsJSON, _ := json.Marshal(char.Tags)
	creatorNotes := char.Data.CreatorNotes
	creator := char.Data.Creator
	charVer := char.Data.CharacterVersion
	createDate := char.CreateDate
	if createDate == nil || createDate == "" || createDate == 0.0 {
		createDate = char.DateAdded
	}
	dateAdded := char.DateAdded
	if dateAdded == 0 || createDate == nil || createDate == "" || createDate == 0.0 {
		var exAdded, exCreated float64
		_ = db.QueryRow("SELECT date_added, create_date FROM characters WHERE user_folder=? AND avatar=?",
			userFolder, char.Avatar).Scan(&exAdded, &exCreated)
		if dateAdded == 0 {
			dateAdded = exAdded
		}
		if createDate == nil || createDate == "" || createDate == 0.0 {
			createDate = exCreated
			if createDate == nil || createDate == 0.0 {
				createDate = dateAdded
			}
		}
	}
	if createDate == nil || createDate == 0.0 {
		createDate = 0.0
	}

	// Import/edit/rename callers only know avatar+name, so an upsert must not
	// wipe the recency already recorded for that character.
	dateLastChat := char.DateLastChat
	if dateLastChat == 0 {
		var existing float64
		_ = db.QueryRow("SELECT date_last_chat FROM characters WHERE user_folder=? AND avatar=?",
			userFolder, char.Avatar).Scan(&existing)
		dateLastChat = existing
	}

	db.Exec(`INSERT OR REPLACE INTO characters
		(user_folder, avatar, name, fav, date_added, create_date, date_last_chat,
		chat_size, data_size, tags, chat, creator, creator_notes, character_version, mtime)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		userFolder, char.Avatar, char.Name, boolToInt(char.Fav),
		dateAdded, createDate, dateLastChat,
		char.ChatSize, char.DataSize, string(tagsJSON),
		char.Chat, creator, creatorNotes, charVer, mtime,
	)
}

// UpdateDateLastChat records when a character was last used without disturbing
// the rest of its cached row.
func (idx *Index) UpdateDateLastChat(userFolder, avatar string, ts float64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.db.Exec("UPDATE characters SET date_last_chat=? WHERE user_folder=? AND avatar=?", ts, userFolder, avatar)
}

func (idx *Index) DeleteCharacter(userFolder, avatar string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.deleteCharacter(userFolder, avatar)
}

func (idx *Index) deleteCharacter(userFolder, avatar string) {
	idx.db.Exec("DELETE FROM characters WHERE user_folder=? AND avatar=?", userFolder, avatar)
}

func (idx *Index) ClearUserIndex(userFolder string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.clearUserIndex(userFolder)
}

func (idx *Index) clearUserIndex(userFolder string) {
	idx.db.Exec("DELETE FROM characters WHERE user_folder=?", userFolder)
}

func (idx *Index) RebuildIndex(userFolder string, processFn func(string, models.UserDirectories, bool) (*models.ShallowCharacter, error), dirs models.UserDirectories) []models.ShallowCharacter {
	return idx.RebuildIndexWithProgress(userFolder, processFn, dirs, nil)
}

func pruneDateAddedFile(userFolder string, valid map[string]struct{}) {
	path := filepath.Join(userFolder, "date_added.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var dict map[string]float64
	if err := json.Unmarshal(data, &dict); err != nil || dict == nil {
		return
	}
	changed := false
	for k := range dict {
		if _, ok := valid[k]; !ok {
			delete(dict, k)
			changed = true
		}
	}
	if !changed {
		return
	}
	out, err := json.MarshalIndent(dict, "", "    ")
	if err != nil {
		return
	}
	_ = util.AtomicWrite(path, out)
}

// RebuildIndexWithProgress is RebuildIndex while reporting how many characters
// have been handled, so a caller can drive a progress bar. onProgress may be nil.
func (idx *Index) RebuildIndexWithProgress(userFolder string, processFn func(string, models.UserDirectories, bool) (*models.ShallowCharacter, error), dirs models.UserDirectories, onProgress func(done, total int)) []models.ShallowCharacter {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.clearUserIndex(userFolder)
	entries, err := os.ReadDir(userFolder)
	if err != nil {
		return nil
	}

	var pngs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			pngs = append(pngs, e.Name())
		}
	}
	keep := make(map[string]struct{}, len(pngs))
	for _, n := range pngs {
		keep[strings.TrimSuffix(n, ".png")] = struct{}{}
	}
	pruneDateAddedFile(userFolder, keep)
	if onProgress != nil {
		onProgress(0, len(pngs))
	}

	// Card parsing is CPU- and IO-bound with no shared state, so it fans out
	// across workers; the SQLite writes stay serial inside one transaction.
	// Results keep directory order: the response must not depend on which
	// worker finished first.
	type rebuildOut struct {
		char *models.ShallowCharacter
	}
	outs := make([]rebuildOut, len(pngs))
	var done atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, name := range pngs {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			char, err := processFn(name, dirs, true)
			if err == nil && char != nil && char.Name != "" {
				outs[i].char = char
			}
			if onProgress != nil {
				onProgress(int(done.Add(1)), len(pngs))
			}
		}(i, name)
	}
	wg.Wait()

	tx, err := idx.db.Begin()
	if err != nil {
		tx = nil
	}
	var db dbExecer = idx.db
	if tx != nil {
		db = tx
	}
	var results []models.ShallowCharacter
	for _, o := range outs {
		if o.char == nil {
			continue
		}
		idx.upsertCharacterDB(db, userFolder, *o.char)
		results = append(results, *o.char)
	}
	if tx != nil {
		_ = tx.Commit()
	}
	FlushDateAddedCache(userFolder)
	return results
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
