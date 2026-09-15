package media

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TurtleTavern/turtletavern/internal/util"
)

const MetadataFile = "image-metadata.json"

type ImageMetadata struct {
	Hash                string   `json:"hash,omitempty"`
	AspectRatio         float64  `json:"aspectRatio,omitempty"`
	IsAnimated          bool     `json:"isAnimated,omitempty"`
	DominantColor       string   `json:"dominantColor,omitempty"`
	FolderIDs           []string `json:"folderIds"`
	AddedTimestamp      int64    `json:"addedTimestamp,omitempty"`
	ThumbnailResolution int      `json:"thumbnailResolution,omitempty"`
	Mtime               float64  `json:"mtime,omitempty"`
}

type MetadataFolder struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ThumbnailFile string `json:"thumbnailFile"`
}

type MetadataIndex struct {
	Version int                      `json:"version"`
	Images  map[string]ImageMetadata `json:"images"`
	Folders []MetadataFolder         `json:"folders"`
}

var metaMapMu sync.Mutex
var userMetaLocks = make(map[string]*sync.Mutex)

func userMetaLock(userRoot string) *sync.Mutex {
	metaMapMu.Lock()
	defer metaMapMu.Unlock()
	m, ok := userMetaLocks[userRoot]
	if !ok {
		m = &sync.Mutex{}
		userMetaLocks[userRoot] = m
	}
	return m
}

func ReadMetadataIndex(userRoot string) MetadataIndex {
	idx := MetadataIndex{Version: 1, Images: map[string]ImageMetadata{}, Folders: []MetadataFolder{}}
	data, err := os.ReadFile(filepath.Join(userRoot, MetadataFile))
	if err != nil {
		return idx
	}
	var parsed MetadataIndex
	if err := json.Unmarshal(data, &parsed); err != nil {
		return idx
	}
	if parsed.Images == nil {
		parsed.Images = map[string]ImageMetadata{}
	}
	if parsed.Folders == nil {
		parsed.Folders = []MetadataFolder{}
	}
	if parsed.Version == 0 {
		parsed.Version = 1
	}
	return parsed
}

func WriteMetadataIndex(userRoot string, idx MetadataIndex) {
	data, err := json.MarshalIndent(idx, "", "    ")
	if err != nil {
		return
	}
	_ = util.AtomicWrite(filepath.Join(userRoot, MetadataFile), data)
}

func toPosix(p string) string {
	return strings.ReplaceAll(p, string(filepath.Separator), "/")
}

func GenerateImageMetadata(filePath string, thumbType ThumbType, resolution int) (ImageMetadata, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ImageMetadata{}, err
	}
	sum := sha256.Sum256(data)
	dims, ok := DetectDimensions(data)
	if !ok || dims.Width <= 0 || dims.Height <= 0 {
		return ImageMetadata{}, fmt.Errorf("could not determine image dimensions")
	}
	animated := false
	switch strings.ToLower(dims.Format) {
	case "gif":
		animated = true
	case "png":
		animated = IsAnimatedAPNG(data)
	case "webp":
		animated = IsAnimatedWebP(data)
	}
	color := "#808080"
	if !animated {
		if img, err := DecodeImage(data); err == nil {
			color = AverageColorHex(img)
		}
	}
	var added int64
	if st, err := os.Stat(filePath); err == nil {
		added = st.ModTime().UnixMilli()
	} else {
		added = nowMilli()
	}
	ratio := float64(dims.Width) / float64(dims.Height)
	ratio = float64(int(ratio*10000)) / 10000
	return ImageMetadata{
		Hash:                fmt.Sprintf("%x", sum),
		AspectRatio:         ratio,
		IsAnimated:          animated,
		DominantColor:       color,
		FolderIDs:           []string{},
		AddedTimestamp:      added,
		ThumbnailResolution: resolution,
	}, nil
}

func GetOrGenerateMetadataBatch(userRoot string, relativePaths []string, thumbType ThumbType, resolution int) (map[string]ImageMetadata, int) {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	results := make(map[string]ImageMetadata, len(relativePaths))
	idx := ReadMetadataIndex(userRoot)
	modified := false
	generated := 0
	for _, rel := range relativePaths {
		posix := toPosix(rel)
		full := filepath.Join(userRoot, rel)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		mtime := float64(st.ModTime().UnixMilli())
		if cached, ok := idx.Images[posix]; ok && cached.Mtime == mtime {
			results[rel] = cached
			continue
		}
		meta, err := GenerateImageMetadata(full, thumbType, resolution)
		if err != nil {
			continue
		}
		meta.Mtime = mtime
		if cached, ok := idx.Images[posix]; ok && cached.FolderIDs != nil {
			meta.FolderIDs = cached.FolderIDs
		}
		idx.Images[posix] = meta
		results[rel] = meta
		modified = true
		generated++
	}
	if modified {
		WriteMetadataIndex(userRoot, idx)
	}
	return results, generated
}

func RemoveMetadata(userRoot, relativePath string) {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	posix := toPosix(relativePath)
	idx := ReadMetadataIndex(userRoot)
	if _, ok := idx.Images[posix]; !ok {
		return
	}
	delete(idx.Images, posix)
	base := posix[strings.LastIndex(posix, "/")+1:]
	for i := range idx.Folders {
		if idx.Folders[i].ThumbnailFile == base {
			idx.Folders[i].ThumbnailFile = ""
		}
	}
	WriteMetadataIndex(userRoot, idx)
}

func RenameMetadata(userRoot, oldRel, newRel string) error {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	oldPosix := toPosix(oldRel)
	newPosix := toPosix(newRel)
	idx := ReadMetadataIndex(userRoot)
	data, ok := idx.Images[oldPosix]
	if !ok {
		return fmt.Errorf("image '%s' not found in metadata", oldRel)
	}
	delete(idx.Images, oldPosix)
	idx.Images[newPosix] = data
	oldBase := oldPosix[strings.LastIndex(oldPosix, "/")+1:]
	newBase := newPosix[strings.LastIndex(newPosix, "/")+1:]
	if oldBase != newBase {
		for i := range idx.Folders {
			if idx.Folders[i].ThumbnailFile == oldBase {
				idx.Folders[i].ThumbnailFile = newBase
			}
		}
	}
	WriteMetadataIndex(userRoot, idx)
	return nil
}

func CleanupOrphanedMetadata(userRoot string) []string {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	var removed []string
	for rel := range idx.Images {
		full, err := filepath.Abs(filepath.Join(userRoot, filepath.FromSlash(rel)))
		if err != nil {
			removed = append(removed, rel)
			delete(idx.Images, rel)
			continue
		}
		base, err := filepath.Abs(userRoot)
		if err != nil {
			continue
		}
		if full != base && !strings.HasPrefix(full, base+string(filepath.Separator)) {
			removed = append(removed, rel)
			delete(idx.Images, rel)
			continue
		}
		if _, err := os.Stat(full); err != nil {
			removed = append(removed, rel)
			delete(idx.Images, rel)
		}
	}
	if len(removed) > 0 {
		WriteMetadataIndex(userRoot, idx)
	}
	if removed == nil {
		removed = []string{}
	}
	return removed
}

func CreateFolder(userRoot, name string) MetadataFolder {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	folder := MetadataFolder{ID: newUUID(), Name: name, ThumbnailFile: ""}
	idx.Folders = append(idx.Folders, folder)
	WriteMetadataIndex(userRoot, idx)
	return folder
}

func SetFolderThumbnailsBatch(userRoot string, updates []FolderThumbUpdate) {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	for _, u := range updates {
		for i := range idx.Folders {
			if idx.Folders[i].ID == u.ID {
				idx.Folders[i].ThumbnailFile = u.ThumbnailFile
			}
		}
	}
	WriteMetadataIndex(userRoot, idx)
}

type FolderThumbUpdate struct {
	ID            string
	ThumbnailFile string
}

func UpdateFolder(userRoot, folderID string, name, thumbnailFile *string) (MetadataFolder, error) {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	for i := range idx.Folders {
		if idx.Folders[i].ID == folderID {
			if name != nil {
				idx.Folders[i].Name = *name
			}
			if thumbnailFile != nil {
				idx.Folders[i].ThumbnailFile = *thumbnailFile
			}
			WriteMetadataIndex(userRoot, idx)
			return idx.Folders[i], nil
		}
	}
	return MetadataFolder{}, fmt.Errorf("folder '%s' not found", folderID)
}

func DeleteFolder(userRoot, folderID string) error {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	pos := -1
	for i := range idx.Folders {
		if idx.Folders[i].ID == folderID {
			pos = i
			break
		}
	}
	if pos == -1 {
		return fmt.Errorf("folder '%s' not found", folderID)
	}
	idx.Folders = append(idx.Folders[:pos], idx.Folders[pos+1:]...)
	for key, meta := range idx.Images {
		kept := meta.FolderIDs[:0]
		for _, id := range meta.FolderIDs {
			if id != folderID {
				kept = append(kept, id)
			}
		}
		if kept == nil {
			kept = []string{}
		}
		meta.FolderIDs = kept
		idx.Images[key] = meta
	}
	WriteMetadataIndex(userRoot, idx)
	return nil
}

func AssignImagesToFolder(userRoot, folderID string, relativePaths []string) error {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	found := false
	for _, f := range idx.Folders {
		if f.ID == folderID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("folder '%s' not found", folderID)
	}
	for _, rp := range relativePaths {
		posix := toPosix(rp)
		normalized := posixNormalize(posix)
		if !strings.HasPrefix(normalized, "backgrounds/") || hasDotDot(normalized) {
			return fmt.Errorf("invalid background path: '%s'", rp)
		}
		if _, err := os.Stat(filepath.Join(userRoot, normalized)); err != nil {
			continue
		}
		meta := idx.Images[normalized]
		if meta.FolderIDs == nil {
			meta.FolderIDs = []string{}
		}
		present := false
		for _, id := range meta.FolderIDs {
			if id == folderID {
				present = true
				break
			}
		}
		if !present {
			meta.FolderIDs = append(meta.FolderIDs, folderID)
		}
		idx.Images[normalized] = meta
	}
	WriteMetadataIndex(userRoot, idx)
	return nil
}

func UnassignImagesFromFolder(userRoot, folderID string, relativePaths []string) {
	ul := userMetaLock(userRoot)
	ul.Lock()
	defer ul.Unlock()
	idx := ReadMetadataIndex(userRoot)
	for _, rp := range relativePaths {
		posix := toPosix(rp)
		meta, ok := idx.Images[posix]
		if !ok || meta.FolderIDs == nil {
			continue
		}
		kept := meta.FolderIDs[:0]
		for _, id := range meta.FolderIDs {
			if id != folderID {
				kept = append(kept, id)
			}
		}
		meta.FolderIDs = kept
		idx.Images[posix] = meta
	}
	WriteMetadataIndex(userRoot, idx)
}

func posixNormalize(p string) string {
	parts := strings.Split(p, "/")
	var out []string
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			} else {
				out = append(out, "..")
			}
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
