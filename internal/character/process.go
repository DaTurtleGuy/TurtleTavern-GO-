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
)

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
	dateAddedFile := filepath.Join(dirs.Characters, "date_added.json")
	dateAddedData := make(map[string]float64)
	if data, err := os.ReadFile(dateAddedFile); err == nil {
		json.Unmarshal(data, &dateAddedData)
	}

	charStat, _ := os.Stat(imgFile)
	ctimeMs := 0.0
	if charStat != nil {
		ctimeMs = float64(charStat.ModTime().UnixMilli())
	}

	dateAdded := ctimeMs
	if v, ok := dateAddedData[fileNameWithoutExt]; ok {
		dateAdded = v
	}
	needSave := false
	if _, ok := dateAddedData[fileNameWithoutExt]; !ok {
		dateAddedData[fileNameWithoutExt] = dateAdded
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
		dataJSON, _ := json.MarshalIndent(dateAddedData, "", "    ")
		_ = util.AtomicWrite(dateAddedFile, dataJSON)
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

	dateAddedFile := filepath.Join(dirs.Characters, "date_added.json")
	dateAddedData := make(map[string]float64)
	if data, err := os.ReadFile(dateAddedFile); err == nil {
		json.Unmarshal(data, &dateAddedData)
	}
	fileNameWithoutExt := strings.TrimSuffix(item, ".png")
	charStat, _ := os.Stat(imgFile)
	ctimeMs := 0.0
	if charStat != nil {
		ctimeMs = float64(charStat.ModTime().UnixMilli())
	}
	dateAdded := ctimeMs
	if v, ok := dateAddedData[fileNameWithoutExt]; ok {
		dateAdded = v
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
