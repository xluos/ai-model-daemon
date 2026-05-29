package proxy

import (
	"strings"
	"testing"
)

// box builds an axis-aligned 4-point polygon from a rectangle.
func box(x0, y0, x1, y1 float64) [][]float64 {
	return [][]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

func TestAggregateGroupsAdjacentLines(t *testing.T) {
	// Three stacked, left-aligned lines (~20px tall, ~5px gaps) form one block.
	items := []ocrItem{
		{Box: box(10, 10, 200, 30), Text: "第一行文字", Confidence: 0.9},
		{Box: box(10, 35, 210, 55), Text: "第二行文字", Confidence: 0.8},
		{Box: box(10, 60, 190, 80), Text: "第三行文字", Confidence: 0.95},
	}
	text, blocks := aggregateOCR(items)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d (%q)", len(blocks), text)
	}
	want := "第一行文字\n第二行文字\n第三行文字"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(blocks[0].Lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(blocks[0].Lines))
	}
}

func TestAggregateKeepsIsolatedBlocksSeparate(t *testing.T) {
	// One line at the top, another far below — must not be glued together.
	items := []ocrItem{
		{Box: box(10, 10, 200, 30), Text: "顶部标题", Confidence: 0.9},
		{Box: box(10, 400, 200, 420), Text: "底部脚注", Confidence: 0.9},
	}
	text, blocks := aggregateOCR(items)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d (%q)", len(blocks), text)
	}
	if !strings.Contains(text, "\n\n") {
		t.Errorf("expected blank-line separator between groups, got %q", text)
	}
}

func TestAggregateSeparatesSideBySideColumns(t *testing.T) {
	// Two columns at the same height but far apart horizontally stay separate.
	items := []ocrItem{
		{Box: box(10, 10, 100, 30), Text: "左栏", Confidence: 0.9},
		{Box: box(500, 10, 600, 30), Text: "右栏", Confidence: 0.9},
	}
	_, blocks := aggregateOCR(items)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks for side-by-side columns, got %d", len(blocks))
	}
}

func TestAggregateMergesSameLineFragments(t *testing.T) {
	// Two fragments on the same line, small horizontal gap — one line, latin
	// fragments get a separating space.
	items := []ocrItem{
		{Box: box(10, 10, 60, 30), Text: "Hello", Confidence: 0.9},
		{Box: box(68, 10, 130, 30), Text: "World", Confidence: 0.9},
	}
	text, blocks := aggregateOCR(items)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if text != "Hello World" {
		t.Errorf("text = %q, want %q", text, "Hello World")
	}
}

func TestAggregateFallbackWithoutBoxes(t *testing.T) {
	items := []ocrItem{
		{Box: [][]float64{}, Text: "整段识别结果", Confidence: 0.9},
	}
	text, blocks := aggregateOCR(items)
	if blocks != nil {
		t.Errorf("expected nil blocks in fallback, got %d", len(blocks))
	}
	if text != "整段识别结果" {
		t.Errorf("text = %q", text)
	}
}
