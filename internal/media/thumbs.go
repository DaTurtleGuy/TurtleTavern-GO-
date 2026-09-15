package media

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

type ThumbType string

const (
	ThumbBG      ThumbType = "bg"
	ThumbAvatar  ThumbType = "avatar"
	ThumbPersona ThumbType = "persona"
)

var skippedThumbExts = map[string]bool{
	".apng": true, ".mp4": true, ".webm": true, ".avi": true,
	".mkv": true, ".flv": true, ".gif": true,
}

type ThumbDirs struct {
	Cache string
	Orig  string
}

func ThumbFolders(root string, thumbType ThumbType) (cache, orig string, ok bool) {
	thumbs := filepath.Join(root, "thumbnails")
	switch thumbType {
	case ThumbBG:
		return filepath.Join(thumbs, "bg"), filepath.Join(root, "backgrounds"), true
	case ThumbAvatar:
		return filepath.Join(thumbs, "avatar"), filepath.Join(root, "characters"), true
	case ThumbPersona:
		return filepath.Join(thumbs, "persona"), filepath.Join(root, "User Avatars"), true
	default:
		return "", "", false
	}
}

func InvalidateThumbnail(root string, thumbType ThumbType, file string) {
	cache, _, ok := ThumbFolders(root, thumbType)
	if !ok {
		return
	}
	_ = os.Remove(filepath.Join(cache, util.SanitizeFileName(file)))
}

func ThumbDimensions(cfg *config.Config, thumbType ThumbType) (int, int) {
	if dims, ok := cfg.Thumbnails.Dimensions[string(thumbType)]; ok && len(dims) == 2 {
		return dims[0], dims[1]
	}
	switch thumbType {
	case ThumbBG:
		return 160, 90
	default:
		return 96, 144
	}
}

func ThumbResolution(cfg *config.Config, thumbType ThumbType) int {
	w, h := ThumbDimensions(cfg, thumbType)
	return w * h
}

func ThumbFormatPNG(cfg *config.Config) bool {
	return strings.ToLower(strings.TrimSpace(cfg.Thumbnails.Format)) == "png"
}

func ThumbQuality(cfg *config.Config) int {
	q := cfg.Thumbnails.Quality
	if q < 1 {
		q = 1
	}
	if q > 100 {
		q = 100
	}
	return q
}

type ThumbResult struct {
	Path        string
	AspectRatio float64
	Resolution  int
	Generated   bool
}

func GenerateThumbnail(cfg *config.Config, root string, thumbType ThumbType, file string, force bool, knownAnimated *bool) ThumbResult {
	if knownAnimated != nil && *knownAnimated {
		return ThumbResult{}
	}
	cacheDir, origDir, ok := ThumbFolders(root, thumbType)
	if !ok {
		return ThumbResult{}
	}
	cachedPath := filepath.Join(cacheDir, file)
	origPath := filepath.Join(origDir, file)
	if !force {
		if st, err := os.Stat(cachedPath); err == nil && !st.IsDir() {
			if origSt, err := os.Stat(origPath); err == nil {
				if !origSt.ModTime().After(st.ModTime()) {
					if dims, ok := thumbDimsOf(cachedPath); ok {
						ratio := 1.0
						if dims.Height > 0 {
							ratio = float64(dims.Width) / float64(dims.Height)
						}
						return ThumbResult{Path: cachedPath, AspectRatio: ratio, Resolution: ThumbResolution(cfg, thumbType), Generated: false}
					}
					return ThumbResult{Path: cachedPath, Resolution: ThumbResolution(cfg, thumbType)}
				}
			} else {
				return ThumbResult{Path: cachedPath, Resolution: ThumbResolution(cfg, thumbType)}
			}
		}
	}
	origData, err := os.ReadFile(origPath)
	if err != nil {
		return ThumbResult{}
	}
	ext := strings.ToLower(filepath.Ext(file))
	if ext == ".webp" && (knownAnimated == nil) {
		if IsAnimatedWebP(origData) {
			return ThumbResult{}
		}
	}
	if ext == ".png" && knownAnimated == nil {
		if IsAnimatedAPNG(origData) {
			return ThumbResult{}
		}
	}
	if skippedThumbExts[ext] {
		return ThumbResult{}
	}
	img, err := DecodeImage(origData)
	if err != nil {
		return ThumbResult{}
	}
	b := img.Bounds()
	aspect := 1.0
	if b.Dy() > 0 {
		aspect = float64(b.Dx()) / float64(b.Dy())
	}
	var out []byte
	if thumbType == ThumbBG {
		w, h := ThumbDimensions(cfg, thumbType)
		scaled, _, _ := FitByArea(img, w*h)
		img = scaled
	} else {
		w, h := ThumbDimensions(cfg, thumbType)
		img = Cover(img, w, h)
	}
	if ThumbFormatPNG(cfg) {
		out, err = EncodePNG(img)
	} else {
		out, err = EncodeJPEG(img, ThumbQuality(cfg))
	}
	if err != nil {
		return ThumbResult{}
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	if err := util.AtomicWrite(cachedPath, out); err != nil {
		return ThumbResult{}
	}
	return ThumbResult{Path: cachedPath, AspectRatio: aspect, Resolution: ThumbResolution(cfg, thumbType), Generated: true}
}

func thumbDimsOf(path string) (Dimensions, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Dimensions{}, false
	}
	return DetectDimensions(data)
}

func IsFirefoxUA(ua string) bool {
	return strings.Contains(strings.ToLower(ua), "firefox")
}
