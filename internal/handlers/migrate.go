package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/util"
)

func MigrateGroupMetadata(dataRoot string, handles []string) {
	for _, handle := range handles {
		root := filepath.Join(dataRoot, handle)
		groupsDir := filepath.Join(root, "groups")
		chatsDir := filepath.Join(root, "group chats")
		entries, err := os.ReadDir(groupsDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			groupPath := filepath.Join(groupsDir, e.Name())
			data, err := os.ReadFile(groupPath)
			if err != nil {
				continue
			}
			var group map[string]any
			if err := json.Unmarshal(data, &group); err != nil {
				continue
			}
			_, hasChatMeta := group["chat_metadata"]
			_, hasPastMeta := group["past_metadata"]
			if !hasChatMeta && !hasPastMeta {
				continue
			}
			backupDir := filepath.Join(root, "backups", "_group_metadata_update")
			_ = os.MkdirAll(backupDir, 0o755)
			if backup, err := os.ReadFile(groupPath); err == nil {
				_ = util.AtomicWrite(filepath.Join(backupDir, e.Name()), backup)
			}
			allMeta := map[string]any{}
			if past, ok := group["past_metadata"].(map[string]any); ok {
				for k, v := range past {
					allMeta[k] = v
				}
			}
			if chatID, ok := group["chat_id"].(string); ok {
				allMeta[chatID] = group["chat_metadata"]
			}
			chats, ok := group["chats"].([]any)
			if !ok {
				continue
			}
			migrated := false
			for _, c := range chats {
				chatID, ok := c.(string)
				if !ok {
					continue
				}
				chatFile := filepath.Join(chatsDir, util.SanitizeFileName(chatID+".jsonl"))
				chatData, err := os.ReadFile(chatFile)
				if err != nil {
					continue
				}
				var lines []any
				for _, line := range strings.Split(string(chatData), "\n") {
					if strings.TrimSpace(line) == "" {
						continue
					}
					var msg map[string]any
					if err := json.Unmarshal([]byte(line), &msg); err == nil {
						lines = append(lines, msg)
					}
				}
				if len(lines) > 0 {
					if first, ok := lines[0].(map[string]any); ok {
						if _, has := first["chat_metadata"]; has {
							continue
						}
					}
				}
				if backup, err := os.ReadFile(chatFile); err == nil {
					_ = util.AtomicWrite(filepath.Join(backupDir, util.SanitizeFileName(chatID+".jsonl")), backup)
				}
				var meta any = map[string]any{}
				if m, ok := allMeta[chatID]; ok && m != nil {
					meta = m
				}
				header := map[string]any{"chat_metadata": meta, "user_name": "unused", "character_name": "unused"}
				var sb strings.Builder
				headerJSON, _ := json.Marshal(header)
				sb.Write(headerJSON)
				for _, entry := range lines {
					entryJSON, _ := json.Marshal(entry)
					sb.WriteString("\n")
					sb.Write(entryJSON)
				}
				_ = util.AtomicWrite(chatFile, []byte(sb.String()))
				migrated = true
			}
			delete(group, "chat_metadata")
			delete(group, "past_metadata")
			if out, err := json.MarshalIndent(group, "", "    "); err == nil {
				_ = util.AtomicWrite(groupPath, out)
			}
			_ = migrated
		}
	}
}
