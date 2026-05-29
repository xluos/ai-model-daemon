package proxy

import (
	"math"
	"sort"
	"strings"
)

// ocrItem is one detected text line as returned by the OCR backends
// (ocr_server.py / rapidocr_server.py): a 4-point polygon, text, confidence.
type ocrItem struct {
	Box        [][]float64 `json:"box"`
	Text       string      `json:"text"`
	Confidence float64     `json:"confidence"`
}

// ocrBlock is a logical group of related text lines reconstructed from the
// scattered detections. Its Text joins the member lines in reading order, one
// line per detected row.
type ocrBlock struct {
	Text       string    `json:"text"`
	Lines      []string  `json:"lines"`
	Confidence float64   `json:"confidence"`
	Box        []float64 `json:"box"` // [minX, minY, maxX, maxY]
}

// boxedLine is an internal representation of one detection with its bounding box.
type boxedLine struct {
	text string
	conf float64
	minX float64
	minY float64
	maxX float64
	maxY float64
}

func (b boxedLine) h() float64  { return b.maxY - b.minY }
func (b boxedLine) cy() float64 { return (b.minY + b.maxY) / 2 }

// Tunable factors, expressed as multiples of the median line height.
const (
	// vGapFactor: vertical gap below which two horizontally-aligned lines are
	// considered the same group (paragraph continuation).
	vGapFactor = 0.8
	// hGapFactor: horizontal gap below which two vertically-aligned fragments
	// are considered the same line.
	hGapFactor = 1.2
)

// aggregateOCR turns scattered OCR detections into reading-order groups.
//
// It is a lightweight, dependency-free geometric heuristic: detections are
// grouped by spatial adjacency (connected components), so genuinely related
// lines are merged in reading order while isolated blocks stay separate.
// Within a group every detected row becomes its own line, joined by "\n";
// distinct groups are separated by a blank line. When detection boxes are
// missing (e.g. use_det=0) it falls back to joining lines in their original
// order.
func aggregateOCR(items []ocrItem) (string, []ocrBlock) {
	boxed := make([]boxedLine, 0, len(items))
	var plainTexts []string
	hasMissingBox := false

	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		plainTexts = append(plainTexts, text)

		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		points := 0
		for _, pt := range it.Box {
			if len(pt) < 2 {
				continue
			}
			x, y := pt[0], pt[1]
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
			points++
		}
		if points == 0 {
			hasMissingBox = true
			continue
		}
		boxed = append(boxed, boxedLine{
			text: text, conf: it.Confidence,
			minX: minX, minY: minY, maxX: maxX, maxY: maxY,
		})
	}

	// Fallback: no geometry to work with — keep original order.
	if hasMissingBox || len(boxed) == 0 {
		return strings.Join(plainTexts, "\n"), nil
	}

	medianH := medianHeight(boxed)
	groups := groupRelated(boxed, medianH)

	blocks := make([]ocrBlock, 0, len(groups))
	for _, g := range groups {
		blocks = append(blocks, buildBlock(g, medianH))
	}
	// Reading order across groups: top-to-bottom, then left-to-right.
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].Box[1] != blocks[j].Box[1] {
			return blocks[i].Box[1] < blocks[j].Box[1]
		}
		return blocks[i].Box[0] < blocks[j].Box[0]
	})

	parts := make([]string, len(blocks))
	for i, b := range blocks {
		parts[i] = b.Text
	}
	return strings.Join(parts, "\n\n"), blocks
}

func medianHeight(boxed []boxedLine) float64 {
	heights := make([]float64, len(boxed))
	for i, b := range boxed {
		heights[i] = b.h()
	}
	sort.Float64s(heights)
	n := len(heights)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return heights[n/2]
	}
	return (heights[n/2-1] + heights[n/2]) / 2
}

// groupRelated clusters detections into connected components, where two
// detections are linked if they are spatially adjacent and aligned — either
// stacked vertically with horizontal overlap (same paragraph) or side by side
// on the same line. Isolated detections form singleton groups.
func groupRelated(boxed []boxedLine, medianH float64) [][]boxedLine {
	n := len(boxed)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if related(boxed[i], boxed[j], medianH) {
				union(i, j)
			}
		}
	}

	groupsByRoot := make(map[int][]boxedLine)
	for i := 0; i < n; i++ {
		root := find(i)
		groupsByRoot[root] = append(groupsByRoot[root], boxed[i])
	}
	groups := make([][]boxedLine, 0, len(groupsByRoot))
	for _, g := range groupsByRoot {
		groups = append(groups, g)
	}
	return groups
}

// related reports whether two detections belong to the same logical block.
func related(a, b boxedLine, medianH float64) bool {
	if medianH <= 0 {
		medianH = math.Max(a.h(), b.h())
	}
	// Interval overlap; positive means overlap, negative means a gap.
	vo := math.Min(a.maxY, b.maxY) - math.Max(a.minY, b.minY)
	ho := math.Min(a.maxX, b.maxX) - math.Max(a.minX, b.minX)

	switch {
	case vo > 0 && ho > 0:
		// Overlapping in both axes — clearly the same region.
		return true
	case ho > 0 && vo <= 0:
		// Vertically stacked, horizontally aligned: paragraph continuation.
		return -vo < vGapFactor*medianH
	case vo > 0 && ho <= 0:
		// Side by side, vertically aligned: same line split into fragments.
		return -ho < hGapFactor*medianH
	default:
		// Diagonally offset with no shared axis — treat as unrelated.
		return false
	}
}

// buildBlock orders a group into rows (top-to-bottom, left-to-right within a
// row) and renders one line of text per row.
func buildBlock(group []boxedLine, medianH float64) ocrBlock {
	rows := clusterRows(group, medianH)

	lineTexts := make([]string, 0, len(rows))
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	var confSum float64
	count := 0
	for _, row := range rows {
		parts := make([]string, len(row))
		for i, b := range row {
			parts[i] = b.text
			minX, minY = math.Min(minX, b.minX), math.Min(minY, b.minY)
			maxX, maxY = math.Max(maxX, b.maxX), math.Max(maxY, b.maxY)
			confSum += b.conf
			count++
		}
		lineTexts = append(lineTexts, smartJoin(parts))
	}

	conf := 0.0
	if count > 0 {
		conf = confSum / float64(count)
	}
	return ocrBlock{
		Text:       strings.Join(lineTexts, "\n"),
		Lines:      lineTexts,
		Confidence: conf,
		Box:        []float64{minX, minY, maxX, maxY},
	}
}

// clusterRows groups detections of one block into visual rows by vertical
// overlap, ordering rows top-to-bottom and each row left-to-right.
func clusterRows(group []boxedLine, medianH float64) [][]boxedLine {
	sorted := make([]boxedLine, len(group))
	copy(sorted, group)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].cy() < sorted[j].cy() })

	var rows [][]boxedLine
	var curTop, curBot float64
	for _, b := range sorted {
		if len(rows) > 0 {
			overlap := math.Min(b.maxY, curBot) - math.Max(b.minY, curTop)
			minH := math.Min(b.h(), curBot-curTop)
			if minH <= 0 {
				minH = medianH
			}
			if overlap > 0.4*minH {
				last := len(rows) - 1
				rows[last] = append(rows[last], b)
				curTop = math.Min(curTop, b.minY)
				curBot = math.Max(curBot, b.maxY)
				continue
			}
		}
		rows = append(rows, []boxedLine{b})
		curTop, curBot = b.minY, b.maxY
	}

	for _, row := range rows {
		sort.SliceStable(row, func(i, j int) bool { return row[i].minX < row[j].minX })
	}
	return rows
}

// smartJoin concatenates text fragments, inserting a space only at boundaries
// where neither side is a CJK character. This keeps Chinese text tight while
// preserving word separation for Latin scripts.
func smartJoin(parts []string) string {
	var sb strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if sb.Len() == 0 {
			sb.WriteString(p)
			continue
		}
		if !isCJK(lastRune(sb.String())) && !isCJK(firstRune(p)) {
			sb.WriteByte(' ')
		}
		sb.WriteString(p)
	}
	return sb.String()
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}

// isCJK reports whether r belongs to a CJK / fullwidth block where no inter-word
// spacing is expected.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Extension A
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK symbols and punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // Halfwidth and fullwidth forms
		return true
	case r >= 0x3040 && r <= 0x30FF: // Hiragana + Katakana
		return true
	default:
		return false
	}
}
