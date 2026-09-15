package character

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

var charxEmbeddedPrefixes = []string{"embeded://", "embedded://", "__asset:"}

var charxImageExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "webp": true,
	"gif": true, "apng": true, "avif": true, "bmp": true, "jfif": true,
}

var charxSpriteTypes = map[string]bool{"emotion": true, "expression": true}
var charxBackgroundTypes = map[string]bool{"background": true}

var zipSignature = []byte{0x50, 0x4B, 0x03, 0x04}

func findZipStart(data []byte) []byte {
	if idx := bytes.Index(data, zipSignature); idx > 0 {
		return data[idx:]
	}
	return data
}

type CharXAsset struct {
	Type            string
	Name            string
	Ext             string
	ZipPath         string
	Order           int
	StorageCategory string
	BaseName        string
}

type CharXResult struct {
	Card             map[string]any
	Avatar           []byte
	AuxiliaryAssets  []CharXAsset
	ExtractedBuffers map[string][]byte
}

func normalizeZipEntryPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "/") {
		p = p[1:]
	}
	parts := strings.Split(p, "/")
	var out []string
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

func openCharXZip(data []byte) (*zip.Reader, error) {
	data = findZipStart(data)
	return zip.NewReader(bytes.NewReader(data), int64(len(data)))
}

func extractCharXFile(data []byte, name string) []byte {
	zr, err := openCharXZip(data)
	if err != nil {
		return nil
	}
	want := normalizeZipEntryPath(name)
	for _, f := range zr.File {
		if normalizeZipEntryPath(f.Name) == want {
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			capHint := f.UncompressedSize64
			if capHint > 8<<20 {
				capHint = 8 << 20
			}
			buf := make([]byte, 0, capHint)
			tmp := make([]byte, 32768)
			for {
				n, err := rc.Read(tmp)
				if n > 0 {
					buf = append(buf, tmp[:n]...)
				}
				if err != nil {
					break
				}
			}
			rc.Close()
			return buf
		}
	}
	return nil
}

func embeddedZipPath(uri string) string {
	if typeofString(uri) == "" {
		return ""
	}
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range charxEmbeddedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return normalizeZipEntryPath(trimmed[len(prefix):])
		}
	}
	return ""
}

func typeofString(v any) string {
	s, _ := v.(string)
	return s
}

func normExtString(ext string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
}

var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

func charXAssetBaseName(name, fallback string, useHyphens bool) string {
	cleaned := strings.TrimSpace(name)
	if cleaned == "" {
		return strings.ToLower(fallback)
	}
	sep := "_"
	if useHyphens {
		sep = "-"
	}
	base := nonAlnumRun.ReplaceAllString(strings.ToLower(cleaned), sep)
	base = strings.Trim(base, sep)
	if base == "" {
		return strings.ToLower(fallback)
	}
	sanitized := util.SanitizeFileName(base)
	if sanitized == "" {
		return strings.ToLower(fallback)
	}
	return strings.ToLower(sanitized)
}

func stripTrailingImageExt(name, expectedExt string) string {
	if name == "" || expectedExt == "" {
		return name
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "."+expectedExt) {
		return name[:len(name)-len(expectedExt)-1]
	}
	for ext := range charxImageExts {
		if strings.HasSuffix(lower, "."+ext) {
			return name[:len(name)-len(ext)-1]
		}
	}
	return name
}

func ParseCharX(data []byte) (*CharXResult, error) {
	cardBuf := extractCharXFile(data, "card.json")
	if cardBuf == nil {
		return nil, fmt.Errorf("failed to extract card.json from CharX file")
	}
	var card map[string]any
	if err := json.Unmarshal(cardBuf, &card); err != nil {
		return nil, fmt.Errorf("invalid CharX card file")
	}
	if _, ok := card["spec"]; !ok {
		return nil, fmt.Errorf("invalid CharX card file: missing spec field")
	}
	var embedded []CharXAsset
	if dataBlock, ok := card["data"].(map[string]any); ok {
		if assets, ok := dataBlock["assets"].([]any); ok {
			for i, item := range assets {
				am, ok := item.(map[string]any)
				if !ok {
					continue
				}
				uri, _ := am["uri"].(string)
				zipPath := embeddedZipPath(uri)
				if zipPath == "" {
					continue
				}
				metaExt, _ := am["ext"].(string)
				pathExt := normExtString(path.Ext(zipPath))
				ext := metaExt
				if ext == "" {
					ext = normExtString(metaExt)
				}
				if ext == "" {
					ext = pathExt
				} else {
					ext = normExtString(ext)
				}
				assetType, _ := am["type"].(string)
				assetType = strings.ToLower(assetType)
				name, _ := am["name"].(string)
				embedded = append(embedded, CharXAsset{
					Type: assetType, Name: name, Ext: ext, ZipPath: zipPath, Order: i,
				})
			}
		}
	}

	var iconAsset *CharXAsset
	for i := range embedded {
		a := &embedded[i]
		if a.Type == "icon" && charxImageExts[a.Ext] && a.ZipPath != "" {
			if iconAsset == nil {
				iconAsset = a
			}
			if strings.ToLower(a.Name) == "main" {
				iconAsset = a
				break
			}
		}
	}

	var auxiliary []CharXAsset
	for _, a := range embedded {
		if a.ZipPath == "" || !charxImageExts[strings.ToLower(a.Ext)] {
			continue
		}
		if a.Type == "icon" || a.Type == "user_icon" {
			continue
		}
		var category string
		switch {
		case charxSpriteTypes[a.Type]:
			category = "sprite"
		case charxBackgroundTypes[a.Type]:
			category = "background"
		default:
			category = "misc"
		}
		useHyphens := category == "sprite"
		nameNoExt := stripTrailingImageExt(a.Name, strings.ToLower(a.Ext))
		base := charXAssetBaseName(nameNoExt, fmt.Sprintf("%s-%d", category, a.Order), useHyphens)
		a.Ext = strings.ToLower(a.Ext)
		a.StorageCategory = category
		a.BaseName = base
		auxiliary = append(auxiliary, a)
	}

	needed := map[string]bool{}
	if iconAsset != nil && iconAsset.ZipPath != "" {
		needed[iconAsset.ZipPath] = true
	}
	for _, a := range auxiliary {
		if a.ZipPath != "" {
			needed[a.ZipPath] = true
		}
	}
	extracted := map[string][]byte{}
	if len(needed) > 0 {
		zr, err := openCharXZip(data)
		if err == nil {
			for _, f := range zr.File {
				norm := normalizeZipEntryPath(f.Name)
				if !needed[norm] {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					continue
				}
				var buf []byte
				tmp := make([]byte, 32768)
				for {
					n, err := rc.Read(tmp)
					if n > 0 {
						buf = append(buf, tmp[:n]...)
					}
					if err != nil {
						break
					}
				}
				rc.Close()
				extracted[norm] = buf
			}
		}
	}

	var avatar []byte
	if iconAsset != nil && iconAsset.ZipPath != "" {
		if buf, ok := extracted[iconAsset.ZipPath]; ok {
			avatar = buf
		}
	}

	return &CharXResult{
		Card: card, Avatar: avatar,
		AuxiliaryAssets: auxiliary, ExtractedBuffers: extracted,
	}, nil
}

type CharXSummary struct {
	Sprites     int
	Backgrounds int
	Misc        int
}

func deleteExistingByBaseName(dirPath, baseName string) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.TrimSuffix(name, filepath.Ext(name)) == baseName {
			_ = os.Remove(filepath.Join(dirPath, name))
		}
	}
}

func PersistCharXAssets(assets []CharXAsset, buffers map[string][]byte, dirs models.UserDirectories, characterFolder string) CharXSummary {
	var summary CharXSummary
	if len(assets) == 0 {
		return summary
	}
	spritesPath := ""
	miscPath := ""
	for _, asset := range assets {
		if asset.ZipPath == "" {
			continue
		}
		buf, ok := buffers[asset.ZipPath]
		if !ok {
			continue
		}
		ext := asset.Ext
		if ext == "" {
			ext = "png"
		}
		switch asset.StorageCategory {
		case "sprite":
			if spritesPath == "" {
				candidate := filepath.Join(dirs.Characters, characterFolder)
				if err := os.MkdirAll(candidate, 0o755); err != nil {
					continue
				}
				spritesPath = candidate
			}
			deleteExistingByBaseName(spritesPath, asset.BaseName)
			if err := util.AtomicWrite(filepath.Join(spritesPath, asset.BaseName+"."+ext), buf); err == nil {
				summary.Sprites++
			}
		case "background":
			bgDir := filepath.Join(dirs.Characters, characterFolder, "backgrounds")
			if err := os.MkdirAll(bgDir, 0o755); err != nil {
				continue
			}
			deleteExistingByBaseName(bgDir, asset.BaseName)
			if err := util.AtomicWrite(filepath.Join(bgDir, asset.BaseName+"."+ext), buf); err == nil {
				summary.Backgrounds++
			}
		case "misc":
			if miscPath == "" {
				candidate := filepath.Join(dirs.UserImages, characterFolder)
				if err := os.MkdirAll(candidate, 0o755); err != nil {
					continue
				}
				miscPath = candidate
			}
			if err := util.AtomicWrite(filepath.Join(miscPath, asset.BaseName+"."+ext), buf); err == nil {
				summary.Misc++
			}
		}
	}
	return summary
}
