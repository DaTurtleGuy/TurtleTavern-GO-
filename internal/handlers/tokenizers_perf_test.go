package handlers

import (
	"strings"
	"testing"
)

func TestCodecCacheDeterministic(t *testing.T) {
	text := "Hello brave world, " + strings.Repeat("lorem ipsum dolor ", 20)
	first, ok := openaiCodec("gpt-4o")
	if !ok {
		t.Skip("codec unavailable")
	}
	ids1, _, err := first.encode(text)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := openaiCodec("gpt-4o")
	if !ok {
		t.Fatal("cache miss on second lookup")
	}
	if first != second {
		t.Fatal("expected the same cached instance")
	}
	ids2, _, err := second.encode(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids1) != len(ids2) {
		t.Fatalf("different lengths: %d vs %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Fatalf("different id at %d", i)
		}
	}
	back, err := second.decode(ids1)
	if err != nil {
		t.Fatal(err)
	}
	if back != text {
		t.Fatalf("roundtrip mismatch: %q", back)
	}
}

func BenchmarkCodecMiss(b *testing.B) {
	text := strings.Repeat("the quick brown fox jumps over ", 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		codecCache.Delete("gpt-3.5-turbo")
		cc, ok := openaiCodec("gpt-3.5-turbo")
		if !ok {
			b.Fatal("no codec")
		}
		if _, _, err := cc.encode(text); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodecHit(b *testing.B) {
	text := strings.Repeat("the quick brown fox jumps over ", 50)
	if _, ok := openaiCodec("gpt-3.5-turbo"); !ok {
		b.Skip("codec unavailable")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cc, ok := openaiCodec("gpt-3.5-turbo")
		if !ok {
			b.Fatal("no codec")
		}
		if _, _, err := cc.encode(text); err != nil {
			b.Fatal(err)
		}
	}
}
