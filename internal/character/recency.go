package character

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// How much of a chat file to read when hunting for its last message. Chat
// records are one JSON object per line, so the tail holds the newest one and a
// multi-gigabyte chat never has to be read in full.
const tailReadBytes = 256 << 10

// ChatStats returns the total size of the chat files in charDir and the time of
// the most recent activity in it.
//
// Recency prefers the last message's send_date and falls back to the file
// modification time only for chats that carry no timestamped message. mtime on
// its own is not trustworthy: a restore, import or backup copy rewrites it,
// which made every chat look equally recent and collapsed "sort by recent" into
// an A-Z list.
func ChatStats(charDir string) (int64, float64) {
	entries, err := os.ReadDir(charDir)
	if err != nil {
		return 0, 0
	}
	var size int64
	var recency float64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size += info.Size()
		ts := chatFileRecency(filepath.Join(charDir, e.Name()), float64(info.ModTime().UnixMilli()))
		if ts > recency {
			recency = ts
		}
	}
	return size, recency
}

// ChatFileRecency reports when a single chat file was last used: the last
// message's send_date when it has one, otherwise the file's modification time.
func ChatFileRecency(path string) float64 {
	fallback := 0.0
	if info, err := os.Stat(path); err == nil {
		fallback = float64(info.ModTime().UnixMilli())
	}
	return chatFileRecency(path, fallback)
}

func chatFileRecency(path string, fallback float64) float64 {
	if ts, ok := lastSendDate(path); ok {
		return ts
	}
	return fallback
}

func lastSendDate(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return 0, false
	}
	start := info.Size() - tailReadBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return 0, false
	}

	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var msg struct {
			SendDate json.Number `json:"send_date"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.SendDate == "" {
			continue
		}
		if ts, err := msg.SendDate.Float64(); err == nil {
			return ts, true
		}
	}
	return 0, false
}
