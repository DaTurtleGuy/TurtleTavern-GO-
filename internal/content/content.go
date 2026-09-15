package content

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TurtleTavern/turtletavern/internal/util"
)

const (
	TypeSettings     = "settings"
	TypeCharacter    = "character"
	TypeSprites      = "sprites"
	TypeBackground   = "background"
	TypeWorld        = "world"
	TypeAvatar       = "avatar"
	TypeTheme        = "theme"
	TypeWorkflow     = "workflow"
	TypeKoboldPreset = "kobold_preset"
	TypeOpenAIPreset = "openai_preset"
	TypeNovelPreset  = "novel_preset"
	TypeTextgenPreset = "textgen_preset"
	TypeInstruct     = "instruct"
	TypeContext      = "context"
	TypeMovingUI     = "moving_ui"
	TypeQuickReplies = "quick_replies"
	TypeSysprompt    = "sysprompt"
	TypeReasoning    = "reasoning"
)

type Item struct {
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Folder   string `json:"folder,omitempty"`
}

var (
	defaultDirOnce sync.Once
	defaultDir     string
)

func DefaultDir() string {
	defaultDirOnce.Do(func() {
		defaultDir = util.ResolveAppPath("default")
	})
	return defaultDir
}

func ContentDir() string  { return filepath.Join(DefaultDir(), "content") }
func ScaffoldDir() string { return filepath.Join(DefaultDir(), "scaffold") }

func loadIndexFile(path, folder string) []Item {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		return nil
	}
	for i := range items {
		items[i].Folder = folder
	}
	return items
}

func GetContentIndex() []Item {
	var result []Item
	result = append(result, loadIndexFile(filepath.Join(ScaffoldDir(), "index.json"), ScaffoldDir())...)
	result = append(result, loadIndexFile(filepath.Join(ContentDir(), "index.json"), ContentDir())...)
	return result
}

func TargetRelDir(itemType string) string {
	switch itemType {
	case TypeSettings:
		return "."
	case TypeCharacter, TypeSprites:
		return "characters"
	case TypeBackground:
		return "backgrounds"
	case TypeWorld:
		return "worlds"
	case TypeAvatar:
		return "User Avatars"
	case TypeTheme:
		return "themes"
	case TypeWorkflow:
		return "user/workflows"
	case TypeKoboldPreset:
		return "KoboldAI Settings"
	case TypeOpenAIPreset:
		return "OpenAI Settings"
	case TypeNovelPreset:
		return "NovelAI Settings"
	case TypeTextgenPreset:
		return "TextGen Settings"
	case TypeInstruct:
		return "instruct"
	case TypeContext:
		return "context"
	case TypeMovingUI:
		return "movingUI"
	case TypeQuickReplies:
		return "QuickReplies"
	case TypeSysprompt:
		return "sysprompt"
	case TypeReasoning:
		return "reasoning"
	default:
		return ""
	}
}

func presetTypes() map[string]bool {
	return map[string]bool{
		TypeInstruct: true, TypeContext: true, TypeSysprompt: true, TypeReasoning: true,
	}
}

func GetDefaultPresets(userRoot string) []Item {
	var presets []Item
	pt := presetTypes()
	for _, item := range GetContentIndex() {
		if strings.HasSuffix(item.Type, "_preset") || pt[item.Type] {
			item.Name = strings.TrimSuffix(filepath.Base(item.Filename), filepath.Ext(item.Filename))
			rel := TargetRelDir(item.Type)
			if rel == "" {
				continue
			}
			item.Folder = filepath.Join(userRoot, rel)
			presets = append(presets, item)
		}
	}
	return presets
}

func GetDefaultPresetFile(filename string) any {
	data, err := os.ReadFile(filepath.Join(ContentDir(), filename))
	if err != nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	return v
}

func contentLog(root string) (string, []string) {
	logPath := filepath.Join(root, "content.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return logPath, nil
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return logPath, out
}

func CheckForNewContent(userRoots []string, forceTypes []string, skipCheck bool) {
	if skipCheck && len(forceTypes) == 0 {
		return
	}
	force := make(map[string]bool, len(forceTypes))
	for _, t := range forceTypes {
		force[t] = true
	}
	index := GetContentIndex()
	for _, root := range userRoots {
		seedContentForUser(index, root, force)
	}
}

func seedContentForUser(index []Item, root string, force map[string]bool) {
	_ = os.MkdirAll(root, 0o755)
	logPath, logged := contentLog(root)
	seen := make(map[string]bool, len(logged))
	for _, f := range logged {
		seen[f] = true
	}
	for _, item := range index {
		if seen[item.Filename] && !force[item.Type] {
			continue
		}
		rel := TargetRelDir(item.Type)
		if rel == "" {
			continue
		}
		targetDir := filepath.Join(root, rel)
		targetPath := filepath.Join(targetDir, filepath.Base(item.Filename))
		seen[item.Filename] = true
		logged = append(logged, item.Filename)
		if _, err := os.Stat(targetPath); err == nil {
			continue
		}
		srcPath := filepath.Join(item.Folder, item.Filename)
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		_ = os.MkdirAll(targetDir, 0o755)
		_ = util.CopyFile(srcPath, targetPath)
	}
	_ = util.AtomicWriteString(logPath, strings.Join(logged, "\n"))
}
