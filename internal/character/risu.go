package character

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

func ImportRisuSprites(dirs models.UserDirectories, data map[string]any) {
	dataBlock, ok := data["data"].(map[string]any)
	if !ok {
		return
	}
	name, _ := dataBlock["name"].(string)
	ext, ok := dataBlock["extensions"].(map[string]any)
	if !ok {
		return
	}
	risuData, ok := ext["risuai"].(map[string]any)
	if !ok || name == "" {
		return
	}
	var images [][]any
	if arr, ok := risuData["additionalAssets"].([]any); ok {
		for _, item := range arr {
			if pair, ok := item.([]any); ok && len(pair) == 2 {
				images = append(images, pair)
			}
		}
	}
	if arr, ok := risuData["emotions"].([]any); ok {
		for _, item := range arr {
			if pair, ok := item.([]any); ok && len(pair) == 2 {
				images = append(images, pair)
			}
		}
	}
	if len(images) == 0 {
		return
	}
	spritesPath := filepath.Join(dirs.Characters, util.SanitizeFileName(name))
	_ = os.MkdirAll(spritesPath, 0o755)
	if st, err := os.Stat(spritesPath); err != nil || !st.IsDir() {
		return
	}
	entries, _ := os.ReadDir(spritesPath)
outer:
	for _, pair := range images {
		label, _ := pair[0].(string)
		fileB64, _ := pair[1].(string)
		if label == "" || fileB64 == "" {
			continue
		}
		for _, e := range entries {
			if strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) == label {
				continue outer
			}
		}
		raw, err := base64.StdEncoding.DecodeString(fileB64)
		if err != nil {
			if raw2, err2 := base64.RawStdEncoding.DecodeString(fileB64); err2 == nil {
				raw = raw2
			} else {
				continue
			}
		}
		_ = util.AtomicWrite(filepath.Join(spritesPath, util.SanitizeFileName(label+".png")), raw)
	}

	delete(risuData, "additionalAssets")
	delete(risuData, "emotions")
}
