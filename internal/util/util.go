package util

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	exeDirOnce     sync.Once
	exeDirPath     string
	appDirOverride string
)

// SetAppDir overrides ExeDir for hosts without an executable directory (mobile).
func SetAppDir(dir string) {
	appDirOverride = dir
}

// ExeDir returns the directory containing the running executable. All
// application-relative paths resolve against this, so the server behaves
// identically no matter which working directory it was launched from.
func ExeDir() string {
	if appDirOverride != "" {
		return appDirOverride
	}
	exeDirOnce.Do(func() {
		exe, err := os.Executable()
		if err == nil {
			// EvalSymlinks uses lstat, which Android's seccomp policy blocks on amd64.
			if runtime.GOOS != "android" {
				if resolved, err := filepath.EvalSymlinks(exe); err == nil {
					exe = resolved
				}
			}
			exeDirPath = filepath.Dir(exe)
			return
		}
		if wd, err := os.Getwd(); err == nil {
			exeDirPath = wd
			return
		}
		exeDirPath = "."
	})
	return exeDirPath
}

// ResolveAppPath makes relative p absolute against the executable directory.
func ResolveAppPath(p string) string {
	if p == "" {
		return ExeDir()
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(ExeDir(), p)
}

func GenerateToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func ReadOrCreateFile(path string, nBytes int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return data, nil
	}
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return nil, fmt.Errorf("create dir %s: %w", dir, err)
	}
	secret, err := GenerateToken(nBytes)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return []byte(secret), nil
}

func HumanizedDateTime(timestampMS int64) string {
	if timestampMS == 0 {
		timestampMS = time.Now().UnixMilli()
	}
	t := time.UnixMilli(timestampMS)
	return fmt.Sprintf("%04d-%02d-%02d@%02dh%02dm%02ds%03dms",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), int64(t.Nanosecond())/int64(time.Millisecond))
}

func GenerateTimestamp() string {
	now := time.Now()
	return fmt.Sprintf("%04d%02d%02d-%02d%02d%02d",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second())
}

func TryParseJSON(s string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

func TryReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func TryReadFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TryDeleteFile(path string) bool {
	if err := os.Remove(path); err != nil {
		return false
	}
	return true
}

func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func AtomicWriteString(path string, data string) error {
	return AtomicWrite(path, []byte(data))
}

func ReadFirstLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// bufio.Reader (not Scanner): Scanner's 64 KiB token cap aborts on long
	// first lines, which chat metadata can exceed.
	reader := bufio.NewReader(f)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func IsPathUnderParent(parent, child string) bool {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	absChild, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absChild)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func DeepMerge(target, source map[string]any) map[string]any {
	output := make(map[string]any, len(target))
	for k, v := range target {
		output[k] = v
	}
	for k, srcVal := range source {
		if srcMap, ok := srcVal.(map[string]any); ok {
			if tgtVal, exists := output[k]; exists {
				if tgtMap, ok := tgtVal.(map[string]any); ok {
					output[k] = DeepMerge(tgtMap, srcMap)
					continue
				}
			}
		}
		output[k] = srcVal
	}
	return output
}

func FormatBytes(numBytes int64) string {
	if numBytes == 0 {
		return "0 Bytes"
	}
	const unit = 1024
	sizes := []string{"Bytes", "KB", "MB", "GB", "TB"}
	i := 0
	divisor := float64(1)
	for f := float64(numBytes); f >= unit && i < len(sizes)-1; f /= unit {
		i++
		divisor *= unit
	}
	return strconv.FormatFloat(float64(numBytes)/divisor, 'f', 2, 64) + " " + sizes[i]
}

var unsafeFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func SanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		name = "unnamed"
	}
	return name
}

var sanitizedReplacer = strings.NewReplacer(
	"<", "_", ">", "_", ":", "_", "\"", "_",
	"/", "_", "\\", "_", "|", "_", "?", "_", "*", "_",
)

func SanitizeSafeReplacer(_ string) string {
	return "_"
}

func RemoveOldBackups(directory, prefix string, limit int) {
	if limit <= 0 {
		limit = 50
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	type named struct {
		name string
		time int64
	}
	var matches []named
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			info, err := e.Info()
			if err != nil {
				continue
			}
			matches = append(matches, named{name: e.Name(), time: info.ModTime().UnixMilli()})
		}
	}
	if len(matches) <= limit {
		return
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].time < matches[j].time })
	for len(matches) > limit {
		oldest := matches[0]
		matches = matches[1:]
		os.Remove(filepath.Join(directory, oldest.name))
	}
}

func MutateJSONString(jsonString string, fn func(map[string]any)) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(jsonString), &obj); err != nil {
		return jsonString
	}
	fn(obj)
	data, err := json.Marshal(obj)
	if err != nil {
		return jsonString
	}
	return string(data)
}

func UnsetPrivateFields(card map[string]any) {
	card["fav"] = false
	if data, ok := card["data"].(map[string]any); ok {
		if ext, ok := data["extensions"].(map[string]any); ok {
			ext["fav"] = false
		}
	}
	delete(card, "chat")
}

func ClientRelativePath(root, inputPath string) string {
	if !strings.HasPrefix(inputPath, root) {
		return inputPath
	}
	rel := inputPath[len(root):]
	return strings.ReplaceAll(rel, "\\", "/")
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func EnsureDirectory(dirPath string) bool {
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return false
	}
	return true
}

func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func MoveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return copyDirRecursive(src, dst)
}

func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return CopyFile(path, target)
	})
}

func RemoveDirAll(path string) error {
	return os.RemoveAll(path)
}

func ReadJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func WriteJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data)
}

func GetPngName(name string, charactersDir string) string {
	base := name
	i := 1
	for FileExists(filepath.Join(charactersDir, name+".png")) {
		name = fmt.Sprintf("%s%d", base, i)
		i++
	}
	return name
}

func GetNameFromAvatar(avatar string) string {
	return strings.TrimSuffix(avatar, ".png")
}

func CalculateDataSize(data any) int {
	if data == nil {
		return 0
	}
	b, err := json.Marshal(data)
	if err != nil {
		return 0
	}
	return len(b)
}
