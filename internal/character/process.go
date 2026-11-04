package character

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

var (
	charCache  = sync.Map{}
	cacheStats = sync.Map{}
	dateCaches = sync.Map{}
)

// DateAddedCache holds one characters dir's date_added.json in memory so a
// rebuild doesn't re-read and re-parse the whole file per character. Every
// snapshot written is a consistent view (marshal under lock), so concurrent
// flushes converge instead of clobbering each other.
type DateAddedCache struct {
	mu    sync.Mutex
	path  string
	data  map[string]float64
	dirty bool
}

func dateCacheFor(charactersDir string) *DateAddedCache {
	if c, ok := dateCaches.Load(charactersDir); ok {
		return c.(*DateAddedCache)
	}
	c := &DateAddedCache{
		path: filepath.Join(charactersDir, "date_added.json"),
		data: map[string]float64{},
	}
	if raw, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(raw, &c.data)
		if c.data == nil {
			c.data = map[string]float64{}
		}
	}
	actual, _ := dateCaches.LoadOrStore(charactersDir, c)
	return actual.(*DateAddedCache)
}

// FlushDateAddedCache writes the shared cache for dir if it changed. Called
// once at the end of a rebuild; single ProcessCharacter calls flush inline.
func FlushDateAddedCache(charactersDir string) {
	if c, ok := dateCaches.Load(charactersDir); ok {
		c.(*DateAddedCache).Flush()
	}
}

func (c *DateAddedCache) Get(name string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[name]
	return v, ok
}

func (c *DateAddedCache) Set(name string, ts float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[name] = ts
	c.dirty = true
}

func (c *DateAddedCache) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[name]; ok {
		delete(c.data, name)
		c.dirty = true
	}
}

func (c *DateAddedCache) Move(oldName, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if oldName == "" || newName == "" || oldName == newName {
		return
	}
	v, ok := c.data[oldName]
	if !ok {
		return
	}
	if _, exists := c.data[newName]; !exists {
		c.data[newName] = v
	}
	delete(c.data, oldName)
	c.dirty = true
}

// Prune keeps only keys present in valid, dropping entries for deleted files
// from both the in-memory cache and (on Flush) the file.
func (c *DateAddedCache) Prune(valid map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if _, ok := valid[k]; !ok {
			delete(c.data, k)
			c.dirty = true
		}
	}
}

// SetDateAddedEntry records a creation date through the shared cache so the
// file and the in-memory view stay consistent. It flushes inline: callers are
// single add/duplicate/import operations, while bulk rebuilds flush once at
// the end via FlushDateAddedCache.
func SetDateAddedEntry(charactersDir, base string, ms float64) {
	if base == "" {
		return
	}
	c := dateCacheFor(charactersDir)
	c.Set(base, ms)
	c.Flush()
}

// MoveDateAddedEntry carries the creation date across a rename through the
// shared cache.
func MoveDateAddedEntry(charactersDir, oldBase, newBase string) {
	if oldBase == "" || newBase == "" || oldBase == newBase {
		return
	}
	c := dateCacheFor(charactersDir)
	c.Move(oldBase, newBase)
	c.Flush()
}

// RemoveDateAddedEntry drops a creation date through the shared cache.
func RemoveDateAddedEntry(charactersDir, base string) {
	if base == "" {
		return
	}
	c := dateCacheFor(charactersDir)
	c.Remove(base)
	c.Flush()
}

func (c *DateAddedCache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	if out, err := json.MarshalIndent(c.data, "", "    "); err == nil {
		_ = util.AtomicWrite(c.path, out)
	}
	c.dirty = false
}

// cardCreateDateMillis reads the card's own embedded create_date as epoch
// milliseconds. It is the best creation evidence after the oldest message:
// unlike the PNG mtime it survives restores (it lives inside the file bytes),
// so a group-chat-only character with no solo chats keeps its real import date
// instead of being stamped with the restore copy time.
func cardCreateDateMillis(charJSON map[string]any) (float64, bool) {
	switch v := charJSON["create_date"].(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v)); err == nil {
			ms := float64(ts.UnixMilli())
			if ms > 0 {
				return ms, true
			}
		}
		return 0, false
	case float64:
		if v > 1e11 {
			return v, true
		}
		if v > 0 {
			return v * 1000, true
		}
	}
	return 0, false
}

func ProcessCharacter(item string, dirs models.UserDirectories, shallow bool) (*models.ShallowCharacter, error) {
	imgFile := filepath.Join(dirs.Characters, item)
	imgData, err := ReadCharacterDataFromFile(imgFile)
	if err != nil || imgData == "" {
		return nil, fmt.Errorf("failed to read character file: %s", item)
	}

	charJSON := make(map[string]any)
	if err := json.Unmarshal([]byte(imgData), &charJSON); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", item, err)
	}

	fileNameWithoutExt := strings.TrimSuffix(item, ".png")
	cache := dateCacheFor(dirs.Characters)
	charStat, _ := os.Stat(imgFile)
	dateAdded, hasEntry := cache.Get(fileNameWithoutExt)
	needSave := false
	// A stored 0 is not a date: it is a poisoned entry (legacy birthtime-less
	// file, a send_date of 0, or a stat failure at write time). It must be
	// recomputed like a miss, otherwise the 0 sticks forever: rebuilds reuse
	// it via hasEntry, and characters without solo chats would otherwise have
	// nothing that could correct it, so "Newest" buries them at the bottom.
	if !hasEntry || dateAdded <= 0 {
		// Evidence ranking: oldest solo message first (a card cannot predate
		// it), then the card's own embedded create_date (survives restores,
		// so group-chat-only characters keep their real import date), then
		// the file mtime (correct for fresh imports; a restore copy time for
		// anything else, so it stays strictly last).
		if oldest, ok := ChatOldestSendDate(filepath.Join(dirs.Chats, fileNameWithoutExt)); ok && oldest > 0 {
			dateAdded = oldest
		} else if cardDate, ok := cardCreateDateMillis(charJSON); ok {
			dateAdded = cardDate
		} else if charStat != nil {
			dateAdded = float64(charStat.ModTime().UnixMilli())
		} else {
			dateAdded = float64(time.Now().UnixMilli())
		}
		cache.Set(fileNameWithoutExt, dateAdded)
		needSave = true
	}

	result, wasFixed := GetCharaCardV2(charJSON, dirs)
	result["avatar"] = item

	if wasFixed {
		if fixedJSON, err := json.Marshal(result); err == nil {
			imgBuf, readErr := os.ReadFile(imgFile)
			if readErr == nil {
				if outBuf, wErr := WriteCharacterDataToPNG(imgBuf, string(fixedJSON)); wErr == nil {
					util.AtomicWrite(imgFile, outBuf)
				}
			}
		}
		result["json_data"] = imgData
	} else {
		result["json_data"] = imgData
	}

	result["create_date"] = dateAdded
	result["date_added"] = dateAdded

	chatsDir := filepath.Join(dirs.Chats, fileNameWithoutExt)
	chatSize, dateLastChat := ChatStats(chatsDir)

	result["chat_size"] = chatSize
	result["date_last_chat"] = dateLastChat
	result["data_size"] = EstimateCharacterTokens(result["data"])
	result["json_data"] = imgData

	if needSave {
		cache.Flush()
	}

	if shallow {
		return toShallow(result), nil
	}
	return toShallow(result), nil
}

func ProcessCharacterFull(item string, dirs models.UserDirectories) (map[string]any, error) {
	imgFile := filepath.Join(dirs.Characters, item)
	imgData, err := ReadCharacterDataFromFile(imgFile)
	if err != nil || imgData == "" {
		return nil, fmt.Errorf("failed to read character file: %s", item)
	}
	charJSON := make(map[string]any)
	if err := json.Unmarshal([]byte(imgData), &charJSON); err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", item, err)
	}

	fileNameWithoutExt := strings.TrimSuffix(item, ".png")
	charStat, _ := os.Stat(imgFile)
	cache := dateCacheFor(dirs.Characters)
	dateAdded, hasEntry := cache.Get(fileNameWithoutExt)
	if !hasEntry || dateAdded <= 0 {
		if oldest, ok := ChatOldestSendDate(filepath.Join(dirs.Chats, fileNameWithoutExt)); ok && oldest > 0 {
			dateAdded = oldest
		} else if cardDate, ok := cardCreateDateMillis(charJSON); ok {
			dateAdded = cardDate
		} else if charStat != nil {
			dateAdded = float64(charStat.ModTime().UnixMilli())
		} else {
			dateAdded = float64(time.Now().UnixMilli())
		}
		// Persist like ProcessCharacter does: a single Get is not part of a
		// bulk rebuild, so flushing inline keeps file and cache consistent.
		cache.Set(fileNameWithoutExt, dateAdded)
		cache.Flush()
	}

	result, wasFixed := GetCharaCardV2(charJSON, dirs)
	result["avatar"] = item

	if wasFixed {
		if fixedJSON, err := json.Marshal(result); err == nil {
			imgBuf, readErr := os.ReadFile(imgFile)
			if readErr == nil {
				if outBuf, wErr := WriteCharacterDataToPNG(imgBuf, string(fixedJSON)); wErr == nil {
					util.AtomicWrite(imgFile, outBuf)
				}
			}
		}
	}

	result["create_date"] = dateAdded
	result["date_added"] = dateAdded

	chatsDir := filepath.Join(dirs.Chats, fileNameWithoutExt)
	chatSize, dateLastChat := ChatStats(chatsDir)
	result["chat_size"] = chatSize
	result["date_last_chat"] = dateLastChat
	result["data_size"] = EstimateCharacterTokens(result["data"])
	result["json_data"] = imgData

	return result, nil
}

func WriteCharacterDataToFile(inputImage []byte, jsonData string, outputFile string, dirs models.UserDirectories) error {
	if inputImage == nil {
		inputImage = DefaultAvatarPNG
	}
	outImage, err := WriteCharacterDataToPNG(inputImage, jsonData)
	if err != nil {
		return err
	}
	outPath := filepath.Join(dirs.Characters, outputFile+".png")
	return util.AtomicWrite(outPath, outImage)
}

func toShallow(char map[string]any) *models.ShallowCharacter {
	sc := &models.ShallowCharacter{
		Shallow: true,
		Name:    getString(char, "name"),
		Avatar:  getString(char, "avatar"),
		Chat:    getString(char, "chat"),
	}
	sc.Fav = getBool(char, "fav")
	sc.DateAdded = getFloat64(char, "date_added")
	sc.CreateDate = char["create_date"]
	sc.DateLastChat = getFloat64(char, "date_last_chat")
	sc.ChatSize = getInt64(char, "chat_size")
	sc.DataSize = getInt(char, "data_size")

	tags := []string{}
	if t, ok := char["tags"].([]any); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	sc.Tags = tags

	sc.Data = models.ShallowData{
		Name:             sc.Name,
		Tags:             tags,
		CharacterVersion: getDataString(char, "character_version"),
		Creator:          getDataString(char, "creator"),
		CreatorNotes:     getDataString(char, "creator_notes"),
	}
	sc.Data.Extensions.Fav = sc.Fav
	return sc
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getFloat64(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if f, ok := toFloat64(v); ok {
			return f
		}
	}
	return 0
}

// toFloat64 accepts every numeric kind a map value can hold. Values inserted by
// this package are concrete Go ints (ChatStats returns int64, CalculateDataSize
// returns int), while values decoded from JSON are float64. Asserting only
// float64 silently collapsed chat_size and data_size to 0 for every character,
// which made the frontend's "Most/Least chats" and "Most/Least tokens" sorts
// compare 0 - 0 for every card and fall back to the server's directory order.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

func getInt64(m map[string]any, key string) int64 {
	return int64(getFloat64(m, key))
}

func getInt(m map[string]any, key string) int {
	return int(getFloat64(m, key))
}

func getDataString(char map[string]any, key string) string {
	data, ok := char["data"].(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
