package handlers

import (
	"context"
	"strings"
	"testing"
	"time"
)

type prefixTranslator struct {
	prefix string
}

func (p *prefixTranslator) TranslateBatch(_ context.Context, texts []string, _ string, _ string) ([]string, error) {
	out := make([]string, len(texts))
	for i, text := range texts {
		out[i] = p.prefix + text
	}
	return out, nil
}

func TestTranslateVTTContentIncremental_TranslatesSingleAndMultiLineCueText(t *testing.T) {
	input := strings.Join([]string{
		"WEBVTT",
		"",
		"1",
		"00:00.000 --> 00:01.000",
		"Hello world.",
		"",
		"00:01.500 --> 00:03.000",
		"First line",
		"Second line",
		"",
		"custom-id",
		"00:03.500 --> 00:05.000",
		"Third line",
		"",
	}, "\n")

	got, _, err := translateVTTContentIncremental(
		context.Background(),
		input,
		"en",
		"de",
		&prefixTranslator{prefix: "DE: "},
		nil,
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("translateVTTContentIncremental returned error: %v", err)
	}

	assertContains := func(needle string) {
		t.Helper()
		if !strings.Contains(got, needle) {
			t.Fatalf("translated VTT missing %q\n--- got ---\n%s", needle, got)
		}
	}
	assertNotContains := func(needle string) {
		t.Helper()
		if strings.Contains(got, needle) {
			t.Fatalf("translated VTT should not contain %q\n--- got ---\n%s", needle, got)
		}
	}

	// Cue identifiers must remain unmodified.
	assertContains("\n1\n00:00.000 --> 00:01.000\n")
	assertContains("\ncustom-id\n00:03.500 --> 00:05.000\n")
	assertNotContains("DE: 1")
	assertNotContains("DE: custom-id")

	// Subtitle text lines should all be translated, including lines before next cue timestamps.
	assertContains("DE: Hello world.")
	assertContains("DE: First line")
	assertContains("DE: Second line")
	assertContains("DE: Third line")
}

func TestTranslateVTTContentIncremental_StripsMarkupBeforeTranslation(t *testing.T) {
	input := strings.Join([]string{
		"WEBVTT",
		"",
		"00:00.000 --> 00:01.000",
		"<b>Hello</b> <i>there</i>",
		"",
		"00:01.000 --> 00:02.000",
		"<b>m 276 20 l 264 20 l 264 -120</b>",
		"",
	}, "\n")

	got, _, err := translateVTTContentIncremental(
		context.Background(),
		input,
		"en",
		"de",
		&prefixTranslator{prefix: "DE: "},
		nil,
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("translateVTTContentIncremental returned error: %v", err)
	}

	if !strings.Contains(got, "DE: Hello there") {
		t.Fatalf("expected translated cleaned cue text, got:\n%s", got)
	}
	if strings.Contains(got, "<b>") || strings.Contains(got, "<i>") {
		t.Fatalf("expected markup tags to be stripped before translation, got:\n%s", got)
	}
	if strings.Contains(got, "DE: m 276 20 l 264 20 l 264 -120") {
		t.Fatalf("expected ASS vector draw path to be dropped, got:\n%s", got)
	}
}

func TestTranslateVTTContentIncremental_ReturnsCachedLinesWhenNoNewCandidates(t *testing.T) {
	input := strings.Join([]string{
		"WEBVTT",
		"",
		"00:00.000 --> 00:01.000",
		"Hello world.",
		"",
		"00:01.500 --> 00:03.000",
		"Second line",
		"",
	}, "\n")

	cached := map[string]string{
		lineCacheKey("Hello world.", "en", "de"): "Hallo Welt.",
		lineCacheKey("Second line", "en", "de"):  "Zweite Zeile",
	}

	got, _, err := translateVTTContentIncremental(
		context.Background(),
		input,
		"en",
		"de",
		&prefixTranslator{prefix: "DE: "},
		cached,
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("translateVTTContentIncremental returned error: %v", err)
	}

	if strings.Contains(got, "Hello world.") || strings.Contains(got, "Second line") {
		t.Fatalf("expected cached translated lines, got source text:\n%s", got)
	}
	if !strings.Contains(got, "Hallo Welt.") || !strings.Contains(got, "Zweite Zeile") {
		t.Fatalf("expected cached translated lines in output, got:\n%s", got)
	}
}
