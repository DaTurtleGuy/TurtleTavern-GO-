package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TurtleTavern/turtletavern/internal/config"
)

// oldSaveEncode is the pre-optimization algorithm, frozen as a test oracle:
// decode into maps, re-marshal each message, join with newlines.
func oldSaveEncode(t *testing.T, chatData []map[string]any) string {
	t.Helper()
	var sb strings.Builder
	for i, msg := range chatData {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("oracle marshal failed: %v", err)
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.Write(b)
	}
	return sb.String()
}

func testChatHandler() *ChatHandler {
	return &ChatHandler{Cfg: &config.Config{}}
}

func TestTrySaveChatByteIdentical(t *testing.T) {
	msgs := []map[string]any{
		{"chat_metadata": map[string]any{"integrity": "slug-1"}, "user_name": "u", "character_name": "c"},
		{"name": "c", "is_user": false, "mes": "héllo wörld ✓ key order matters", "extra": map[string]any{"display_text": ""}, "send_date": "123"},
		{"name": "u", "is_user": true, "mes": "  spaced  ", "swipes": []any{"a", "b"}, "gen_started": "2024-01-01T00:00:00.000Z"},
	}
	rawBody, err := json.Marshal(map[string]any{"chat": msgs})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Chat []map[string]any `json:"chat"`
	}
	if err := json.Unmarshal(rawBody, &wire); err != nil {
		t.Fatal(err)
	}
	want := oldSaveEncode(t, wire.Chat)

	var rawChat []json.RawMessage
	// Simulate the handler path: decode body.chat as raw messages.
	var body struct {
		Chat []json.RawMessage `json:"chat"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatal(err)
	}
	rawChat = body.Chat
	if !chatElementsAreObjects(rawChat) {
		t.Fatal("valid chat rejected")
	}

	dir := t.TempDir()
	got := filepath.Join(dir, "chat.jsonl")
	h := testChatHandler()
	if err := h.trySaveChat(rawChat, got, "handle", "card", dir, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("save output differs:\n got: %q\nwant: %q", data, want)
	}
}

func TestStreamChatArray(t *testing.T) {
	content := strings.Join([]string{
		`{"name":"a","mes":"one"}`,
		``,
		`   `,
		`not json at all`,
		`[1,2]`,
		`null`,
		`{"name":"b","mes":"two","extra":{"x":1}}`,
		`{"huge":"` + strings.Repeat("x", 200000) + `"}`,
	}, "\n")
	path := filepath.Join(t.TempDir(), "chat.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n := streamChatArray(&buf, path)
	if n != 4 {
		t.Fatalf("got %d messages, want 4 (two objects + null + huge)", n)
	}
	var gen []any
	if err := json.Unmarshal(buf.Bytes(), &gen); err != nil {
		t.Fatalf("streamed output is not a JSON array: %v\n%s", err, buf.String())
	}
	if len(gen) != 4 {
		t.Fatalf("got %d array elements, want 4", len(gen))
	}
	if gen[0].(map[string]any)["mes"] != "one" {
		t.Fatalf("first element mangled: %v", gen[0])
	}
	if gen[1] != nil {
		t.Fatalf("null element not preserved: %v", gen[1])
	}
}

func TestStreamChatArrayMissing(t *testing.T) {
	var buf bytes.Buffer
	if n := streamChatArray(&buf, filepath.Join(t.TempDir(), "nope.jsonl")); n != -1 {
		t.Fatalf("got %d, want -1 for missing file", n)
	}
}

func TestCheckChatIntegrity(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.jsonl", "{\"chat_metadata\":{\"integrity\":\"abc\"}}\n{\"mes\":\"hi\"}\n")
	if !checkChatIntegrity(good, "abc") {
		t.Fatal("matching slug rejected")
	}
	if checkChatIntegrity(good, "other") {
		t.Fatal("mismatched slug accepted")
	}
	noMeta := write("nometa.jsonl", "{\"mes\":\"hi\"}\n")
	if !checkChatIntegrity(noMeta, "abc") {
		t.Fatal("missing metadata should skip the check")
	}
	if !checkChatIntegrity(filepath.Join(dir, "missing.jsonl"), "abc") {
		t.Fatal("missing file should skip the check")
	}
	// First line far beyond the old whole-file read and any scanner limit.
	huge := write("huge.jsonl", "{\"chat_metadata\":{\"integrity\":\"z\"},\"pad\":\""+strings.Repeat("y", 300000)+"\"}\n{\"mes\":\"hi\"}\n")
	if !checkChatIntegrity(huge, "z") {
		t.Fatal("huge first line not handled")
	}
}

func TestChatElementsAreObjects(t *testing.T) {
	ok := []json.RawMessage{json.RawMessage(`{"a":1}`), json.RawMessage(`null`)}
	if !chatElementsAreObjects(ok) {
		t.Fatal("objects+null rejected")
	}
	for _, bad := range []string{`[1]`, `42`, `"str"`, `true`, ``, `   `} {
		if chatElementsAreObjects([]json.RawMessage{json.RawMessage(bad)}) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func benchChat(t *testing.B, n int) ([]map[string]any, []json.RawMessage) {
	t.Helper()
	maps := make([]map[string]any, 0, n+1)
	maps = append(maps, map[string]any{"chat_metadata": map[string]any{}, "user_name": "u", "character_name": "c"})
	for i := 0; i < n; i++ {
		maps = append(maps, map[string]any{
			"name": "c", "is_user": i%2 == 0, "mes": "message number with some roleplay text " + strings.Repeat("lorem ", 40),
			"send_date": "1700000000000", "extra": map[string]any{},
		})
	}
	rawBody, err := json.Marshal(map[string]any{"chat": maps})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Chat []json.RawMessage `json:"chat"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatal(err)
	}
	return maps, body.Chat
}

func BenchmarkSaveOld(b *testing.B) {
	maps, _ := benchChat(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sb strings.Builder
		for j, msg := range maps {
			m, _ := json.Marshal(msg)
			if j > 0 {
				sb.WriteByte('\n')
			}
			sb.Write(m)
		}
		_ = sb.String()
	}
}

func BenchmarkSaveNew(b *testing.B) {
	_, raw := benchChat(b, 2000)
	h := testChatHandler()
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.trySaveChat(raw, filepath.Join(dir, "c.jsonl"), "h", "c", dir, true)
	}
}

func BenchmarkStreamGet(b *testing.B) {
	_, raw := benchChat(b, 2000)
	path := filepath.Join(b.TempDir(), "c.jsonl")
	var sb strings.Builder
	for i, r := range raw {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.Write(r)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		streamChatArray(io.Discard, path)
	}
}
