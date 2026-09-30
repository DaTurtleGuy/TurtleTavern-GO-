package character

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

var (
	charCache   = sync.Map{}
	cacheStats  = sync.Map{}
	dateCaches  = sync.Map{}
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
	if !hasEntry {
		// A missing entry must not be filled from the PNG mtime: after a restore that
		// is the copy time, and it is how creation dates ended up newer than a card's
		// own oldest message. Earliest message evidence wins; the file time is only a
		// last resort for a character with no chats at all.
		if oldest, ok := ChatOldestSendDate(filepath.Join(dirs.Chats, fileNameWithoutExt)); ok {
			dateAdded = oldest
		} else if charStat != nil {
			dateAdded = float64(charStat.ModTime().UnixMilli())
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
	result["data_size"] = util.CalculateDataSize(result["data"])
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
	dateAdded, hasEntry := dateCacheFor(dirs.Characters).Get(fileNameWithoutExt)
	if !hasEntry {
		if oldest, ok := ChatOldestSendDate(filepath.Join(dirs.Chats, fileNameWithoutExt)); ok {
			dateAdded = oldest
		} else if charStat != nil {
			dateAdded = float64(charStat.ModTime().UnixMilli())
		}
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
	result["data_size"] = util.CalculateDataSize(result["data"])
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
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
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
