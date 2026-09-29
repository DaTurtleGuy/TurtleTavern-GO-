package character

// Profiling harness for cold index rebuilds. Gated behind TT_PROFILE=1 so the
// normal suite stays fast. TT_POOL points at a characters dir full of PNGs.
//
// The pool is staged into a scratch dir via hardlinks (same volume, ~free),
// so ProcessCharacter's writes (date_added.json, card fixes) never touch the
// source pool and every run starts cold.
import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/models"
)

func profilePoolDir(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("TT_POOL"); p != "" {
		return p
	}
	return `C:\Users\Julian\AppData\Local\Temp\opencode\bench\libs\tier-1000\default-user\characters`
}

func setupProfileRebuild(t *testing.T) (*Index, models.UserDirectories) {
	t.Helper()
	pool := profilePoolDir(t)
	entries, err := os.ReadDir(pool)
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "chars")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Link(filepath.Join(pool, e.Name()), filepath.Join(scratch, e.Name())); err != nil {
			t.Fatalf("hardlink: %v", err)
		}
		n++
	}
	t.Logf("staged %d files via hardlink", n)
	chats := filepath.Join(t.TempDir(), "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx, models.UserDirectories{Characters: scratch, Chats: chats}
}

func TestProfileSerialRebuild(t *testing.T) {
	if os.Getenv("TT_PROFILE") == "" {
		t.Skip("set TT_PROFILE=1 to run")
	}
	idx, dirs := setupProfileRebuild(t)
	var parseTotal atomic.Int64
	var parseCount atomic.Int64
	wrapped := func(name string, d models.UserDirectories, shallow bool) (*models.ShallowCharacter, error) {
		start := time.Now()
		c, err := ProcessCharacter(name, d, shallow)
		parseTotal.Add(time.Since(start).Nanoseconds())
		parseCount.Add(1)
		return c, err
	}
	totalStart := time.Now()
	out := idx.RebuildIndexWithProgress(dirs.Characters, wrapped, dirs, nil)
	total := time.Since(totalStart)
	parse := time.Duration(parseTotal.Load())
	count := parseCount.Load()
	t.Logf("files=%d total=%s parse=%s upsert+other=%s avg-parse=%s",
		len(out), total, parse, total-parse, parse/time.Duration(count))
}
