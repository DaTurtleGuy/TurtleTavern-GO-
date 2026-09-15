package handlers

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/TurtleTavern/turtletavern/internal/media"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

var assetFilenameRe = regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)

var unsafeExtensions = map[string]bool{
	".php": true, ".exe": true, ".com": true, ".dll": true, ".pif": true,
	".application": true, ".gadget": true, ".msi": true, ".jar": true, ".cmd": true,
	".bat": true, ".reg": true, ".sh": true, ".py": true, ".js": true, ".jse": true,
	".jsp": true, ".pdf": true, ".html": true, ".htm": true, ".hta": true, ".vb": true,
	".vbs": true, ".vbe": true, ".cpl": true, ".msc": true, ".scr": true, ".sql": true,
	".iso": true, ".img": true, ".dmg": true, ".ps1": true, ".ps1xml": true, ".ps2": true,
	".ps2xml": true, ".psc1": true, ".psc2": true, ".msh": true, ".msh1": true,
	".msh2": true, ".mshxml": true, ".msh1xml": true, ".msh2xml": true, ".scf": true,
	".lnk": true, ".inf": true, ".doc": true, ".docm": true, ".docx": true, ".dot": true,
	".dotm": true, ".dotx": true, ".xls": true, ".xlsm": true, ".xlsx": true, ".xlt": true,
	".xltm": true, ".xltx": true, ".xlam": true, ".ppt": true, ".pptm": true, ".pptx": true,
	".pot": true, ".potm": true, ".potx": true, ".ppam": true, ".ppsx": true, ".ppsm": true,
	".pps": true, ".sldx": true, ".sldm": true, ".ws": true,
}

var imageExts = map[string]bool{
	".bmp": true, ".png": true, ".jpg": true, ".webp": true, ".jpeg": true,
	".jfif": true, ".gif": true,
}

var videoExts = map[string]bool{
	".mp4": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true,
	".webm": true, ".3gp": true, ".mkv": true, ".mpg": true,
}

var audioExts = map[string]bool{
	".mp3": true, ".wav": true, ".ogg": true, ".flac": true, ".aac": true,
	".m4a": true, ".aiff": true,
}

const (
	mediaImage = 1
	mediaVideo = 2
	mediaAudio = 4
)

func validateAssetFileName(name string) (bool, string) {
	if !assetFilenameRe.MatchString(name) {
		return false, "Illegal character in filename; only alphanumeric, '_', '-' are accepted."
	}
	if unsafeExtensions[strings.ToLower(filepath.Ext(name))] {
		return false, "Forbidden file extension."
	}
	if strings.HasPrefix(name, ".") {
		return false, "Filename cannot start with '.'"
	}
	if util.SanitizeFileName(name) != name {
		return false, "Reserved or long filename."
	}
	return true, ""
}

func isValidURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func getImages(dir, sortBy string, mediaType int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		switch {
		case mediaType&mediaImage != 0 && imageExts[ext]:
			out = append(out, e.Name())
		case mediaType&mediaVideo != 0 && videoExts[ext]:
			out = append(out, e.Name())
		case mediaType&mediaAudio != 0 && audioExts[ext]:
			out = append(out, e.Name())
		}
	}
	if sortBy == "date" {
		modTimes := make(map[string]int64, len(out))
		for _, name := range out {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
				modTimes[name] = st.ModTime().UnixNano()
			}
		}
		sort.Slice(out, func(i, j int) bool {
			ti, oki := modTimes[out[i]]
			tj, okj := modTimes[out[j]]
			if !oki || !okj {
				return out[i] < out[j]
			}
			return ti < tj
		})
	} else {
		sort.Strings(out)
	}
	return out
}

func clientRelativePath(root, p string) string {
	rel := strings.TrimPrefix(p, root)
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}

func userCtx(r *http.Request) *models.UserContext {
	return auth.UserFromRequest(r)
}

func multipartAvatarFile(w http.ResponseWriter, r *http.Request) (data []byte, filename string, ok bool) {
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, "", false
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["avatar"]) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return nil, "", false
	}
	fh := r.MultipartForm.File["avatar"][0]
	f, err := fh.Open()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, "", false
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, "", false
	}
	return data, fh.Filename, true
}

type MediaHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewMediaHandler(cfg *config.Config) *MediaHandler {
	return &MediaHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg)}
}

func (h *MediaHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/backgrounds", func(r chi.Router) {
		r.Post("/all", h.BackgroundsAll)
		r.Post("/folders", h.BackgroundsFolders)
		r.Post("/delete", h.BackgroundDelete)
		r.Post("/rename", h.BackgroundRename)
		r.Post("/upload", h.BackgroundUpload)
	})
	r.Route("/api/avatars", func(r chi.Router) {
		r.Post("/get", h.AvatarsGet)
		r.Post("/delete", h.AvatarDelete)
		r.Post("/upload", h.AvatarUpload)
	})
	r.Route("/api/images", func(r chi.Router) {
		r.Post("/upload", h.ImageUpload)
		r.Post("/list", h.ImageList)
		r.Post("/list/{folder}", h.ImageList)
		r.Post("/folders", h.ImageFolders)
		r.Post("/delete", h.ImageDelete)
	})
	r.Route("/api/files", func(r chi.Router) {
		r.Post("/sanitize-filename", h.SanitizeFilename)
		r.Post("/upload", h.FileUpload)
		r.Post("/delete", h.FileDelete)
		r.Post("/verify", h.FileVerify)
	})
	r.Route("/api/sprites", func(r chi.Router) {
		r.Get("/get", h.SpritesGet)
		r.Post("/delete", h.SpriteDelete)
		r.Post("/upload-zip", h.SpriteUploadZip)
		r.Post("/upload", h.SpriteUpload)
	})
	r.Route("/api/assets", func(r chi.Router) {
		r.Post("/get", h.AssetsGet)
		r.Post("/download", h.AssetDownload)
		r.Post("/delete", h.AssetDelete)
		r.Post("/character", h.AssetsCharacter)
	})
	r.Get("/thumbnail/", h.Thumbnail)
	r.Get("/thumbnail", h.Thumbnail)
	r.Route("/api/image-metadata", func(r chi.Router) {
		r.Post("/", h.MetadataGet)
		r.Post("/all", h.MetadataAll)
		r.Post("/cleanup", h.MetadataCleanup)
		r.Post("/folders/get", h.FoldersGet)
		r.Post("/folders/create", h.FolderCreate)
		r.Post("/folders/set-thumbnails", h.FoldersSetThumbnails)
		r.Post("/folders/update", h.FolderUpdate)
		r.Post("/folders/delete", h.FolderDelete)
		r.Post("/folders/assign", h.FolderAssign)
		r.Post("/folders/unassign", h.FolderUnassign)
	})
}

func (h *MediaHandler) BackgroundsAll(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	avatarsDir := filepath.Join(uc.Directories.Root, "backgrounds")
	_ = os.MkdirAll(avatarsDir, 0o755)
	images := getImages(uc.Directories.Backgrounds, "name", mediaImage)
	tw, th := media.ThumbDimensions(h.Cfg, media.ThumbBG)
	type imgMeta struct {
		Filename   string `json:"filename"`
		IsAnimated bool   `json:"isAnimated"`
	}
	relPaths := make([]string, 0, len(images))
	for _, img := range images {
		relPaths = append(relPaths, filepath.Join("backgrounds", img))
	}
	metaMap, _ := media.GetOrGenerateMetadataBatch(uc.Directories.Root, relPaths, media.ThumbBG, media.ThumbResolution(h.Cfg, media.ThumbBG))
	out := make([]imgMeta, 0, len(images))
	for _, img := range images {
		animated := false
		if m, ok := metaMap[filepath.Join("backgrounds", img)]; ok {
			animated = m.IsAnimated
		}
		out = append(out, imgMeta{Filename: img, IsAnimated: animated})
	}
	if out == nil {
		out = []imgMeta{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"images": out, "config": map[string]any{"width": tw, "height": th},
	})
}

func (h *MediaHandler) BackgroundsFolders(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	idx := media.ReadMetadataIndex(uc.Directories.Root)
	folders := idx.Folders
	if folders == nil {
		folders = []media.MetadataFolder{}
	}
	imageFolderMap := map[string][]string{}
	for rel, meta := range idx.Images {
		if len(meta.FolderIDs) == 0 {
			continue
		}
		filename := rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			filename = rel[i+1:]
		}
		imageFolderMap[filename] = meta.FolderIDs
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"folders": folders, "imageFolderMap": imageFolderMap})
}

func (h *MediaHandler) BackgroundDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	bg, _ := body["bg"].(string)
	if bg == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !validFileField(body, "bg") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if util.SanitizeFileName(bg) != bg {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	p := filepath.Join(uc.Directories.Backgrounds, util.SanitizeFileName(bg))
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = os.Remove(p)
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbBG, bg)
	media.RemoveMetadata(uc.Directories.Root, filepath.Join("backgrounds", bg))
	_, _ = w.Write([]byte("ok"))
}

func (h *MediaHandler) BackgroundRename(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	oldBG, _ := body["old_bg"].(string)
	newBG, _ := body["new_bg"].(string)
	if oldBG == "" || newBG == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	oldPath := filepath.Join(uc.Directories.Backgrounds, util.SanitizeFileName(oldBG))
	newPath := filepath.Join(uc.Directories.Backgrounds, util.SanitizeFileName(newBG))
	if _, err := os.Stat(oldPath); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := util.AtomicWrite(newPath, data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = os.Remove(oldPath)
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbBG, oldBG)
	_ = media.RenameMetadata(uc.Directories.Root, filepath.Join("backgrounds", oldBG), filepath.Join("backgrounds", newBG))
	_, _ = w.Write([]byte("ok"))
}

func (h *MediaHandler) BackgroundUpload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	data, originalName, ok := multipartAvatarFile(w, r)
	if !ok {
		return
	}
	defer r.MultipartForm.RemoveAll()
	filename := util.SanitizeFileName(originalName)
	if err := util.AtomicWrite(filepath.Join(uc.Directories.Backgrounds, filename), data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbBG, filename)
	_, _ = media.GetOrGenerateMetadataBatch(uc.Directories.Root,
		[]string{filepath.Join("backgrounds", filename)}, media.ThumbBG,
		media.ThumbResolution(h.Cfg, media.ThumbBG))
	_, _ = w.Write([]byte(filename))
}

func (h *MediaHandler) AvatarsGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	images := getImages(uc.Directories.Avatars, "name", mediaImage)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(images)
}

func (h *MediaHandler) AvatarDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	avatar, _ := body["avatar"].(string)
	if avatar == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !validFileField(body, "avatar") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if util.SanitizeFileName(avatar) != avatar {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	p := filepath.Join(uc.Directories.Avatars, util.SanitizeFileName(avatar))
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = os.Remove(p)
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbPersona, util.SanitizeFileName(avatar))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"result":"ok"}`))
}

type avatarCrop struct {
	X          int  `json:"x"`
	Y          int  `json:"y"`
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	WantResize bool `json:"want_resize"`
}

func cropAvatarImage(data []byte, rawCrop string) ([]byte, error) {
	img, err := media.DecodeImage(data)
	if err != nil {
		return nil, err
	}
	finalW, finalH := img.Bounds().Dx(), img.Bounds().Dy()
	if rawCrop != "" {
		var crop avatarCrop
		if err := json.Unmarshal([]byte(rawCrop), &crop); err == nil {
			if crop.Width > 0 && crop.Height > 0 {
				img = media.CropImage(img, crop.X, crop.Y, crop.Width, crop.Height)
				if crop.WantResize {
					finalW, finalH = 512, 768
				} else {
					finalW, finalH = crop.Width, crop.Height
				}
			}
		}
	}
	img = media.Cover(img, finalW, finalH)
	return media.EncodePNG(img)
}

func (h *MediaHandler) AvatarUpload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if r.MultipartForm == nil || len(r.MultipartForm.File["avatar"]) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	fh := r.MultipartForm.File["avatar"][0]
	f, err := fh.Open()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	out, err := cropAvatarImage(data, r.URL.Query().Get("crop"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Is not a valid image"))
		return
	}
	overwrite := ""
	if r.MultipartForm.Value != nil {
		if v := r.MultipartForm.Value["overwrite_name"]; len(v) > 0 {
			overwrite = v[0]
		}
	}
	if overwrite != "" && !validFileField(map[string]any{"overwrite_name": overwrite}, "overwrite_name") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if overwrite != "" {
		media.InvalidateThumbnail(uc.Directories.Root, media.ThumbPersona, util.SanitizeFileName(overwrite))
	}
	filename := util.SanitizeFileName(overwrite)
	if filename == "" {
		filename = util.SanitizeFileName(newAvatarName())
	}
	if err := util.AtomicWrite(filepath.Join(uc.Directories.Avatars, filename), out); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": filename})
}

func (h *MediaHandler) ImageUpload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	imageB64, _ := body["image"].(string)
	format, _ := body["format"].(string)
	if imageB64 == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "No image data provided"})
		return
	}
	if !imageExts["."+strings.ToLower(format)] && format != "jfif" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid image format"})
		return
	}
	var filename string
	if fn, ok := body["filename"].(string); ok && fn != "" {
		base := strings.TrimSuffix(fn, filepath.Ext(fn))
		filename = base + "." + format
	} else {
		filename = newImageName() + "." + format
	}
	dest := filepath.Join(uc.Directories.UserImages, util.SanitizeFileName(filename))
	if chName, ok := body["ch_name"].(string); ok && chName != "" {
		dest = filepath.Join(uc.Directories.UserImages, util.SanitizeFileName(chName), util.SanitizeFileName(filename))
	}
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	raw, err := base64.StdEncoding.DecodeString(imageB64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save the image"})
		return
	}
	if err := util.AtomicWrite(dest, raw); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save the image"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": clientRelativePath(uc.Directories.Root, dest)})
}

func (h *MediaHandler) ImageList(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	if folder := chi.URLParam(r, "folder"); folder != "" {
		if _, has := body["folder"]; has {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Folder specified in both URL and body"})
			return
		}
		body["folder"] = folder
	}
	folder, _ := body["folder"].(string)
	if folder == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "No folder specified"})
		return
	}
	dirPath := filepath.Join(uc.Directories.UserImages, util.SanitizeFileName(folder))
	mediaType := mediaImage
	if n, ok := body["type"].(float64); ok {
		mediaType = int(n)
	}
	sortField, _ := body["sortField"].(string)
	if sortField == "" {
		sortField = "date"
	}
	order, _ := body["sortOrder"].(string)
	_ = os.MkdirAll(dirPath, 0o755)
	images := getImages(dirPath, sortField, mediaType)
	if order == "desc" {
		for i, j := 0, len(images)-1; i < j; i, j = i+1, j-1 {
			images[i], images[j] = images[j], images[i]
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(images)
}

func (h *MediaHandler) ImageFolders(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	_ = os.MkdirAll(uc.Directories.UserImages, 0o755)
	entries, err := os.ReadDir(uc.Directories.UserImages)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unable to retrieve folders"})
		return
	}
	var folders []string
	for _, e := range entries {
		if e.IsDir() {
			folders = append(folders, e.Name())
		}
	}
	if folders == nil {
		folders = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(folders)
}

func (h *MediaHandler) ImageDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	p, _ := body["path"].(string)
	if p == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No path specified"))
		return
	}
	full := filepath.Join(uc.Directories.Root, filepath.FromSlash(p))
	absBase, _ := filepath.Abs(uc.Directories.UserImages)
	absFull, _ := filepath.Abs(full)
	if absFull != absBase && !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Invalid path"))
		return
	}
	if _, err := os.Stat(absFull); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("File not found"))
		return
	}
	_ = os.Remove(absFull)
	w.WriteHeader(http.StatusOK)
}

func (h *MediaHandler) SanitizeFilename(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	name, _ := body["fileName"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No fileName specified"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"fileName": util.SanitizeFileName(name)})
}

func (h *MediaHandler) FileUpload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	dataB64, _ := body["data"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No upload name specified"))
		return
	}
	if dataB64 == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No upload data specified"))
		return
	}
	if ok, msg := validateAssetFileName(name); !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(msg))
		return
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = os.MkdirAll(uc.Directories.Files, 0o755)
	dest := filepath.Join(uc.Directories.Files, name)
	if err := util.AtomicWrite(dest, raw); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": clientRelativePath(uc.Directories.Root, dest)})
}

func (h *MediaHandler) FileDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	p, _ := body["path"].(string)
	if p == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No path specified"))
		return
	}
	full := filepath.Join(uc.Directories.Root, filepath.FromSlash(p))
	absBase, _ := filepath.Abs(uc.Directories.Files)
	absFull, _ := filepath.Abs(full)
	if absFull != absBase && !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Invalid path"))
		return
	}
	if _, err := os.Stat(absFull); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("File not found"))
		return
	}
	_ = os.Remove(absFull)
	w.WriteHeader(http.StatusOK)
}

func (h *MediaHandler) FileVerify(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	urls, ok := body["urls"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("No URLs specified"))
		return
	}
	verified := map[string]bool{}
	for _, u := range urls {
		s, ok := u.(string)
		if !ok {
			continue
		}
		full := filepath.Join(uc.Directories.Root, filepath.FromSlash(s))
		absBase, _ := filepath.Abs(uc.Directories.Files)
		absFull, _ := filepath.Abs(full)
		if absFull != absBase && !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
			continue
		}
		_, err := os.Stat(absFull)
		verified[s] = err == nil
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(verified)
}

func millisName() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

func newAvatarName() string { return millisName() + ".png" }
func newImageName() string  { return millisName() }

func spritesPathFor(charactersDir, name string, isSubfolder bool) string {
	if isSubfolder {
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return ""
		}
		charName := util.SanitizeFileName(parts[0])
		sub := util.SanitizeFileName(parts[1])
		if charName == "" || sub == "" {
			return ""
		}
		return filepath.Join(charactersDir, charName, sub)
	}
	safe := util.SanitizeFileName(name)
	if safe == "" {
		return ""
	}
	return filepath.Join(charactersDir, safe)
}

var spriteLabelRe = regexp.MustCompile(`^(.+?)(?:[-\\.].*?)?$`)

func (h *MediaHandler) SpritesGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	name := r.URL.Query().Get("name")
	isSubfolder := strings.Contains(name, "/")
	spritesPath := spritesPathFor(uc.Directories.Characters, name, isSubfolder)
	type sprite struct {
		Label string `json:"label"`
		Path  string `json:"path"`
	}
	out := []sprite{}
	if spritesPath != "" {
		if st, err := os.Stat(spritesPath); err == nil && st.IsDir() {
			if entries, err := os.ReadDir(spritesPath); err == nil {
				for _, e := range entries {
					if e.IsDir() || !media.IsImageFile(e.Name()) {
						continue
					}
					full := filepath.Join(spritesPath, e.Name())
					st, err := os.Stat(full)
					if err != nil {
						continue
					}
					mtime := st.ModTime().UTC().Format("20060102150405")
					base := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
					label := base
					if m := spriteLabelRe.FindStringSubmatch(base); len(m) == 2 {
						label = m[1]
					}
					out = append(out, sprite{Label: label, Path: "/characters/" + name + "/" + e.Name() + "?t=" + mtime})
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *MediaHandler) SpriteDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	label, _ := body["label"].(string)
	name, _ := body["name"].(string)
	spriteName, _ := body["spriteName"].(string)
	if spriteName == "" {
		spriteName = label
	}
	if spriteName == "" || name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	spritesPath := spritesPathFor(uc.Directories.Characters, name, strings.Contains(name, "/"))
	if spritesPath == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if st, err := os.Stat(spritesPath); err != nil || !st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entries, err := os.ReadDir(spritesPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) == spriteName {
			_ = os.Remove(filepath.Join(spritesPath, e.Name()))
		}
	}
	w.WriteHeader(http.StatusOK)
}

func zipImageBuffers(zipPath string) ([][2]any, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var out [][2]any
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !media.IsImageFile(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		out = append(out, [2]any{filepath.Base(f.Name), data})
	}
	return out, nil
}

func (h *MediaHandler) SpriteUploadZip(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if r.MultipartForm == nil || len(r.MultipartForm.File["avatar"]) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := ""
	if r.MultipartForm.Value != nil {
		if v := r.MultipartForm.Value["name"]; len(v) > 0 {
			name = v[0]
		}
	}
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	spritesPath := spritesPathFor(uc.Directories.Characters, name, strings.Contains(name, "/"))
	if spritesPath == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = os.MkdirAll(spritesPath, 0o755)
	if st, err := os.Stat(spritesPath); err != nil || !st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	fh := r.MultipartForm.File["avatar"][0]
	f, err := fh.Open()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	tmp, err := os.CreateTemp("", "sprites-*.zip")
	if err != nil {
		f.Close()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	_, err = io.Copy(tmp, f)
	f.Close()
	tmp.Close()
	if err != nil {
		_ = os.Remove(tmpName)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sprites, err := zipImageBuffers(tmpName)
	_ = os.Remove(tmpName)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	entries, _ := os.ReadDir(spritesPath)
	for _, s := range sprites {
		filename, _ := s[0].(string)
		buf, _ := s[1].([]byte)
		base := strings.TrimSuffix(filename, filepath.Ext(filename))
		for _, e := range entries {
			if strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) == base {
				_ = os.Remove(filepath.Join(spritesPath, e.Name()))
			}
		}
		_ = util.AtomicWrite(filepath.Join(spritesPath, util.SanitizeFileName(filename)), buf)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "count": len(sprites)})
}

func (h *MediaHandler) SpriteUpload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if r.MultipartForm == nil || len(r.MultipartForm.File["avatar"]) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	vals := map[string]string{}
	if r.MultipartForm.Value != nil {
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				vals[k] = v[0]
			}
		}
	}
	label := vals["label"]
	name := vals["name"]
	spriteName := vals["spriteName"]
	if spriteName == "" {
		spriteName = label
	}
	if label == "" || name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	spritesPath := spritesPathFor(uc.Directories.Characters, name, strings.Contains(name, "/"))
	if spritesPath == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = os.MkdirAll(spritesPath, 0o755)
	if st, err := os.Stat(spritesPath); err != nil || !st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entries, err := os.ReadDir(spritesPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) == spriteName {
			_ = os.Remove(filepath.Join(spritesPath, e.Name()))
		}
	}
	fh := r.MultipartForm.File["avatar"][0]
	src, err := fh.Open()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer src.Close()
	filename := util.SanitizeFileName(spriteName + filepath.Ext(fh.Filename))
	dst, err := os.Create(filepath.Join(spritesPath, filename))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(dst, src)
	dst.Close()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

var validAssetCategories = []string{"bgm", "ambient", "blip", "live2d", "vrm", "character", "temp"}

func (h *MediaHandler) ensureAssetFolders(assetsDir string) {
	for _, cat := range validAssetCategories {
		p := filepath.Join(assetsDir, cat)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			_ = os.Remove(p)
		}
		_ = os.MkdirAll(p, 0o755)
	}
}

func (h *MediaHandler) AssetsGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	output := map[string]any{}
	if st, err := os.Stat(uc.Directories.Assets); err == nil && st.IsDir() {
		h.ensureAssetFolders(uc.Directories.Assets)
		entries, _ := os.ReadDir(uc.Directories.Assets)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			folder := e.Name()
			if folder == "temp" {
				continue
			}
			if folder == "live2d" {
				var found []string
				_ = filepath.WalkDir(filepath.Join(uc.Directories.Assets, folder), func(p string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					if strings.Contains(p, "model") && strings.HasSuffix(p, ".json") {
						found = append(found, clientRelativePath(uc.Directories.Root, p))
					}
					return nil
				})
				if found == nil {
					found = []string{}
				}
				output[folder] = found
				continue
			}
			if folder == "vrm" {
				models := listAssetFiles(filepath.Join(uc.Directories.Assets, "vrm", "model"), uc.Directories.Root, ".placeholder")
				anims := listAssetFiles(filepath.Join(uc.Directories.Assets, "vrm", "animation"), uc.Directories.Root, ".placeholder")
				output["vrm"] = map[string]any{"model": models, "animation": anims}
				continue
			}
			files, _ := os.ReadDir(filepath.Join(uc.Directories.Assets, folder))
			var list []string
			for _, f := range files {
				if f.IsDir() || f.Name() == ".placeholder" {
					continue
				}
				list = append(list, "assets/"+folder+"/"+f.Name())
			}
			if list == nil {
				list = []string{}
			}
			output[folder] = list
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(output)
}

func listAssetFiles(dir, root, skip string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if skip != "" && strings.HasSuffix(p, skip) {
			return nil
		}
		out = append(out, clientRelativePath(root, p))
		return nil
	})
	if out == nil {
		out = []string{}
	}
	return out
}

func (h *MediaHandler) AssetDownload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawURL, _ := body["url"].(string)
	inputCategory, _ := body["category"].(string)
	filename, _ := body["filename"].(string)
	if !isValidURL(rawURL) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	u, _ := url.Parse(rawURL)
	whitelisted := false
	for _, d := range h.Cfg.WhitelistImportDomains {
		if u.Hostname() == d {
			whitelisted = true
			break
		}
	}
	if !whitelisted {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	category := ""
	for _, c := range validAssetCategories {
		if c == inputCategory {
			category = c
			break
		}
	}
	if category == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h.ensureAssetFolders(uc.Directories.Assets)
	if ok, msg := validateAssetFileName(filename); !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(msg))
		return
	}
	tempPath := filepath.Join(uc.Directories.Assets, "temp", filename)
	filePath := filepath.Join(uc.Directories.Assets, category, filename)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	_ = os.Remove(tempPath)
	tmp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(tmp, io.LimitReader(resp.Body, 1<<30))
	tmp.Close()
	if err != nil {
		_ = os.Remove(tempPath)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if category == "character" {
		content, err := os.ReadFile(tempPath)
		if err != nil {
			_ = os.Remove(tempPath)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", mimeByExt(filepath.Ext(tempPath)))
		_, _ = w.Write(content)
		_ = os.Remove(tempPath)
		return
	}
	data, err := os.ReadFile(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := util.AtomicWrite(filePath, data); err != nil {
		_ = os.Remove(tempPath)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = os.Remove(tempPath)
	w.WriteHeader(http.StatusOK)
}

func mimeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".json":
		return "application/json"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func (h *MediaHandler) AssetDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	inputCategory, _ := body["category"].(string)
	filename, _ := body["filename"].(string)
	category := ""
	for _, c := range validAssetCategories {
		if c == inputCategory {
			category = c
			break
		}
	}
	if category == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if ok, msg := validateAssetFileName(filename); !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(msg))
		return
	}
	p := filepath.Join(uc.Directories.Assets, category, filename)
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := os.Remove(p); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *MediaHandler) AssetsCharacter(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	name := r.URL.Query().Get("name")
	category := r.URL.Query().Get("category")
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	safeName := util.SanitizeFileName(name)
	validCat := false
	for _, c := range validAssetCategories {
		if c == category {
			validCat = true
			break
		}
	}
	if !validCat {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	folderPath := filepath.Join(uc.Directories.Characters, safeName, category)
	var output []string
	if st, err := os.Stat(folderPath); err == nil && st.IsDir() {
		if category == "live2d" {
			entries, _ := os.ReadDir(folderPath)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				sub, _ := os.ReadDir(filepath.Join(folderPath, e.Name()))
				for _, f := range sub {
					if strings.Contains(f.Name(), "model") && strings.HasSuffix(f.Name(), ".json") {
						output = append(output, filepath.Join("characters", safeName, category, e.Name(), f.Name()))
					}
				}
			}
		} else {
			entries, _ := os.ReadDir(folderPath)
			for _, e := range entries {
				if !e.IsDir() && e.Name() != ".placeholder" {
					output = append(output, "/characters/"+safeName+"/"+category+"/"+e.Name())
				}
			}
		}
	}
	if output == nil {
		output = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(output)
}

func (h *MediaHandler) Thumbnail(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	rawFile := q.Get("file")
	thumbType := media.ThumbType(q.Get("type"))
	if rawFile == "" || (thumbType != media.ThumbBG && thumbType != media.ThumbAvatar && thumbType != media.ThumbPersona) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if util.SanitizeFileName(rawFile) != rawFile {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	serveOriginal := func() {
		var folder string
		switch thumbType {
		case media.ThumbBG:
			folder = uc.Directories.Backgrounds
		case media.ThumbAvatar:
			folder = uc.Directories.Characters
		default:
			folder = uc.Directories.Avatars
		}
		p := filepath.Join(folder, rawFile)
		if _, err := os.Stat(p); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.maybeFirefoxCache(w, r, p)
		http.ServeFile(w, r, p)
	}
	if !h.Cfg.Thumbnails.Enabled {
		serveOriginal()
		return
	}
	animatedParam := q.Get("animated") == "true"
	ext := strings.ToLower(filepath.Ext(rawFile))
	skippedAnimated := ext == ".apng" || ext == ".mp4" || ext == ".webm" || ext == ".avi" ||
		ext == ".mkv" || ext == ".flv" || ext == ".gif"
	if animatedParam && skippedAnimated {
		serveOriginal()
		return
	}
	if ext == ".gif" {
		serveOriginal()
		return
	}
	cacheDir := map[media.ThumbType]string{
		media.ThumbBG:      filepath.Join(uc.Directories.Root, "thumbnails", "bg"),
		media.ThumbAvatar:  filepath.Join(uc.Directories.Root, "thumbnails", "avatar"),
		media.ThumbPersona: filepath.Join(uc.Directories.Root, "thumbnails", "persona"),
	}[thumbType]
	cached := filepath.Join(cacheDir, rawFile)
	if _, err := os.Stat(cached); err != nil {
		res := media.GenerateThumbnail(h.Cfg, uc.Directories.Root, thumbType, rawFile, false, nil)
		if res.Path == "" {
			serveOriginal()
			return
		}
	}
	if _, err := os.Stat(cached); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.maybeFirefoxCache(w, r, cached)
	http.ServeFile(w, r, cached)
}

func (h *MediaHandler) maybeFirefoxCache(w http.ResponseWriter, r *http.Request, file string) {
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if !strings.Contains(ua, "firefox") {
		return
	}
	if mt := mimeByExt(filepath.Ext(file)); strings.HasPrefix(mt, "image/") {
		w.Header().Set("Cache-Control", "must-understand, no-store")
	}
}

func (h *MediaHandler) MetadataGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	singlePath, _ := body["path"].(string)
	paths, _ := body["paths"].([]any)
	thumbType := media.ThumbType("bg")
	if t, ok := body["type"].(string); ok && t != "" {
		thumbType = media.ThumbType(t)
	}
	if singlePath == "" && paths == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `Either "path" or "paths" is required.`})
		return
	}
	root := uc.Directories.Root
	validate := func(rel string) (string, bool) {
		abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", false
		}
		base, err := filepath.Abs(root)
		if err != nil {
			return "", false
		}
		if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
			return "", false
		}
		return rel, true
	}
	if singlePath != "" && paths == nil {
		rel, ok := validate(singlePath)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Path is outside the user data directory."})
			return
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "File not found."})
			return
		}
		results, _ := media.GetOrGenerateMetadataBatch(root, []string{rel}, thumbType, media.ThumbResolution(h.Cfg, thumbType))
		if meta, ok := results[rel]; ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Could not generate metadata for file."})
		return
	}
	results := map[string]any{}
	var valid []string
	if paths != nil {
		for _, p := range paths {
			s, ok := p.(string)
			if !ok {
				continue
			}
			if rel, ok := validate(s); ok {
				valid = append(valid, rel)
			} else {
				results[s] = map[string]string{"error": "Path is outside the user data directory."}
			}
		}
	}
	batch, _ := media.GetOrGenerateMetadataBatch(root, valid, thumbType, media.ThumbResolution(h.Cfg, thumbType))
	for _, rel := range valid {
		if meta, ok := batch[rel]; ok {
			results[rel] = meta
		} else {
			results[rel] = map[string]string{"error": "File not found or could not process."}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

func (h *MediaHandler) MetadataAll(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	prefix, _ := body["prefix"].(string)
	idx := media.ReadMetadataIndex(uc.Directories.Root)
	if prefix != "" {
		filtered := map[string]media.ImageMetadata{}
		for k, v := range idx.Images {
			if strings.HasPrefix(k, prefix) {
				filtered[k] = v
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": idx.Version, "images": filtered})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(idx)
}

func (h *MediaHandler) MetadataCleanup(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	removed := media.CleanupOrphanedMetadata(uc.Directories.Root)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"removed": removed, "count": len(removed)})
}

func (h *MediaHandler) FoldersGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	idx := media.ReadMetadataIndex(uc.Directories.Root)
	folders := idx.Folders
	if folders == nil {
		folders = []media.MetadataFolder{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(folders)
}

func (h *MediaHandler) FolderCreate(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"name" is required.`})
		return
	}
	folder := media.CreateFolder(uc.Directories.Root, strings.TrimSpace(name))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(folder)
}

func (h *MediaHandler) FoldersSetThumbnails(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawUpdates, ok := body["updates"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"updates" must be an array of {id, thumbnailFile}.`})
		return
	}
	var updates []media.FolderThumbUpdate
	for _, u := range rawUpdates {
		um, ok := u.(map[string]any)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": `"updates" must be an array of {id, thumbnailFile}.`})
			return
		}
		id, _ := um["id"].(string)
		tf, ok := um["thumbnailFile"].(string)
		if id == "" || !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": `"updates" must be an array of {id, thumbnailFile}.`})
			return
		}
		updates = append(updates, media.FolderThumbUpdate{ID: id, ThumbnailFile: tf})
	}
	media.SetFolderThumbnailsBatch(uc.Directories.Root, updates)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *MediaHandler) FolderUpdate(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	id, _ := body["id"].(string)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"id" is required.`})
		return
	}
	var name, thumb *string
	if v, ok := body["name"].(string); ok {
		name = &v
	}
	if v, ok := body["thumbnailFile"].(string); ok {
		thumb = &v
	}
	folder, err := media.UpdateFolder(uc.Directories.Root, id, name, thumb)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(folder)
}

func (h *MediaHandler) FolderDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	id, _ := body["id"].(string)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"id" is required.`})
		return
	}
	if err := media.DeleteFolder(uc.Directories.Root, id); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *MediaHandler) FolderAssign(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	id, _ := body["id"].(string)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"id" is required.`})
		return
	}
	rawPaths, ok := body["paths"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"paths" array is required.`})
		return
	}
	var paths []string
	for _, p := range rawPaths {
		if s, ok := p.(string); ok {
			paths = append(paths, s)
		}
	}
	if err := media.AssignImagesToFolder(uc.Directories.Root, id, paths); err != nil {
		msg := err.Error()
		status := http.StatusInternalServerError
		if strings.Contains(msg, "not found") || strings.Contains(msg, "Invalid background path") {
			if strings.Contains(msg, "not found") {
				status = http.StatusNotFound
			} else {
				status = http.StatusInternalServerError
			}
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *MediaHandler) FolderUnassign(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	id, _ := body["id"].(string)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"id" is required.`})
		return
	}
	rawPaths, ok := body["paths"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `"paths" array is required.`})
		return
	}
	var paths []string
	for _, p := range rawPaths {
		if s, ok := p.(string); ok {
			paths = append(paths, s)
		}
	}
	media.UnassignImagesFromFolder(uc.Directories.Root, id, paths)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
