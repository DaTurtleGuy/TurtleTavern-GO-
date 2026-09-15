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

// A chat holding nothing but metadata has no usage to report. File mtime is not a
// substitute: after a restore every chat in a 1000-character library shares the
// extraction time, and using it fabricated identical "recent" values for all of
// them (measured on a real restored backup).
func TestChatStatsIgnoresMtimeForChatWithoutMessages(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2026, 9, 15, 2, 31, 0, 0, time.UTC)

	writeChat(t, dir, "empty.jsonl", []string{
		`{"user_name":"User","character_name":"Char","create_date":"2024-9-11 @23h 40m 40s 861ms","chat_metadata":{}}`,
	}, mtime)

	_, recency := ChatStats(dir)
	if recency != 0 {
		t.Fatalf("recency = %v (%s), want 0: a metadata-only chat is not recent usage",
			recency, time.UnixMilli(int64(recency)).UTC())
	}
}

func TestChatStatsMissingDir(t *testing.T) {
	size, recency := ChatStats(filepath.Join(t.TempDir(), "nope"))
	if size != 0 || recency != 0 {
		t.Fatalf("missing dir = (%v, %v), want (0, 0)", size, recency)
	}
}

// SillyTavern writes send_date as a human-readable string, not an epoch number.
// Real values from a live library:
//   "February 25, 2025 3:46pm", "October 25, 2025 10:33pm", "November 10, 2025 5:24am"
func TestParseSendDateHumanReadable(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"February 25, 2025 3:46pm", time.Date(2025, 2, 25, 15, 46, 0, 0, time.UTC)},
		{"October 25, 2025 10:33pm", time.Date(2025, 10, 25, 22, 33, 0, 0, time.UTC)},
		{"November 10, 2025 5:24am", time.Date(2025, 11, 10, 5, 24, 0, 0, time.UTC)},
		{"January 5, 2024 12:05pm", time.Date(2024, 1, 5, 12, 5, 0, 0, time.UTC)},
		{"March 9, 2023 12:05am", time.Date(2023, 3, 9, 0, 5, 0, 0, time.UTC)},
		{"Feb 25, 2025 3:46pm", time.Date(2025, 2, 25, 15, 46, 0, 0, time.UTC)},
		{"2026-09-08T12:00:00.000Z", time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, ok := parseSendDate(c.in)
		if !ok {
			t.Errorf("parseSendDate(%q) failed to parse", c.in)
			continue
		}
		if got != float64(c.want.UnixMilli()) {
			t.Errorf("parseSendDate(%q) = %v (%s), want %v (%s)", c.in, got,
				time.UnixMilli(int64(got)).UTC(), float64(c.want.UnixMilli()), c.want.UTC())
		}
	}
}

func TestParseSendDateNumbers(t *testing.T) {
	if got, ok := parseSendDate("1788868800000"); !ok || got != 1788868800000 {
		t.Errorf("epoch ms string = (%v, %v), want 1788868800000", got, ok)
	}
	if got, ok := parseSendDate("1788868800"); !ok || got != 1788868800000 {
		t.Errorf("epoch seconds string = (%v, %v), want 1788868800000", got, ok)
	}
	if _, ok := parseSendDate("not a date"); ok {
		t.Error("garbage parsed as a date")
	}
}

func TestChatStatsParsesHumanReadableSendDate(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, "chat.jsonl", []string{
		`{"user_name":"User","character_name":"Char","chat_metadata":{}}`,
		`{"name":"Char","is_user":false,"send_date":"February 25, 2025 3:46pm","mes":"old"}`,
		`{"name":"Char","is_user":false,"send_date":"October 25, 2025 10:33pm","mes":"newer"}`,
	}, time.Now())

	_, recency := ChatStats(dir)
	want := float64(time.Date(2025, 10, 25, 22, 33, 0, 0, time.UTC).UnixMilli())
	if recency != want {
		t.Fatalf("recency = %v (%s), want the newest human-readable send_date %v (%s)",
			recency, time.UnixMilli(int64(recency)).UTC(), want, time.UnixMilli(int64(want)).UTC())
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
