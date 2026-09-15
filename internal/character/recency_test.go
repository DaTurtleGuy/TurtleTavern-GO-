package character

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeChat(t *testing.T, dir, name string, lines []string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write chat fixture: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// The last message's send_date is the real "last used" time. A chat file whose
// mtime was rewritten by a restore/import/copy must not outrank it.
func TestChatStatsPrefersSendDateOverMtime(t *testing.T) {
	dir := t.TempDir()
	sendDate := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	sendDateNewer := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC).UnixMilli()

	writeChat(t, dir, "old.jsonl", []string{
		`{"user_name":"User","character_name":"Char","chat_metadata":{}}`,
		`{"name":"Char","is_user":false,"send_date":` + itoa(sendDate) + `,"mes":"hi"}`,
	}, time.Now())

	writeChat(t, dir, "newer.jsonl", []string{
		`{"user_name":"User","character_name":"Char","chat_metadata":{}}`,
		`{"name":"Char","is_user":false,"send_date":` + itoa(sendDateNewer) + `,"mes":"yo"}`,
	}, time.Now().Add(-72*time.Hour))

	_, recency := ChatStats(dir)
	if recency != float64(sendDateNewer) {
		t.Fatalf("recency = %v, want the newest send_date %v (mtime must not win)", recency, float64(sendDateNewer))
	}
}

// A chat with no timestamped message has nothing but its file time to go on.
func TestChatStatsFallsBackToMtime(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)

	writeChat(t, dir, "empty.jsonl", []string{
		`{"user_name":"User","character_name":"Char","chat_metadata":{}}`,
	}, mtime)

	_, recency := ChatStats(dir)
	if recency != float64(mtime.UnixMilli()) {
		t.Fatalf("recency = %v, want the file mtime %v", recency, float64(mtime.UnixMilli()))
	}
}

func TestChatStatsMissingDir(t *testing.T) {
	size, recency := ChatStats(filepath.Join(t.TempDir(), "nope"))
	if size != 0 || recency != 0 {
		t.Fatalf("missing dir = (%v, %v), want (0, 0)", size, recency)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
