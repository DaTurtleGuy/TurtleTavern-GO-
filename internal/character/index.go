package character

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TurtleTavern/turtletavern/internal/models"
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

func (idx *Index) UpsertCharacter(userFolder string, char models.ShallowCharacter) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.upsertCharacter(userFolder, char)
}

func (idx *Index) upsertCharacter(userFolder string, char models.ShallowCharacter) {
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
	if createDate == nil || createDate == 0.0 {
		createDate = 0.0
	}

	// Import/edit/rename callers only know avatar+name, so an upsert must not
	// wipe the recency already recorded for that character.
	dateLastChat := char.DateLastChat
	if dateLastChat == 0 {
		var existing float64
		_ = idx.db.QueryRow("SELECT date_last_chat FROM characters WHERE user_folder=? AND avatar=?",
			userFolder, char.Avatar).Scan(&existing)
		dateLastChat = existing
	}

	idx.db.Exec(`INSERT OR REPLACE INTO characters
		(user_folder, avatar, name, fav, date_added, create_date, date_last_chat,
		chat_size, data_size, tags, chat, creator, creator_notes, character_version, mtime)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		userFolder, char.Avatar, char.Name, boolToInt(char.Fav),
		char.DateAdded, createDate, dateLastChat,
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
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.clearUserIndex(userFolder)
	entries, err := os.ReadDir(userFolder)
	if err != nil {
		return nil
	}
	var results []models.ShallowCharacter
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		char, err := processFn(e.Name(), dirs, true)
		if err != nil || char == nil || char.Name == "" {
			continue
		}
		idx.upsertCharacter(userFolder, *char)
		results = append(results, *char)
	}
	return results
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
