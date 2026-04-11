package phos

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

type treeNode struct {
	spanID string
	name   string
	prefix string
}

type markerKind int

const (
	eventMarker markerKind = iota
	errorMarker
)

type markerData struct {
	kind markerKind
	desc string
}

type legendEntry struct {
	key   string
	items []markerData
}

const legendKeys = "123456789abcdefghijklmnopqrstuvwxyz"

// RenderTraces renders all spans grouped by trace in a CLI-friendly timeline.
func RenderTraces(spans []SpanData) string {
	if len(spans) == 0 {
		return ""
	}

	grouped := make(map[string][]SpanData)
	for _, span := range spans {
		key := span.TraceID
		if key == "" {
			key = span.ID
		}
		grouped[key] = append(grouped[key], span)
	}

	traceIDs := make([]string, 0, len(grouped))
	for traceID := range grouped {
		traceIDs = append(traceIDs, traceID)
	}
	sort.Slice(traceIDs, func(i, j int) bool {
		left := firstStart(grouped[traceIDs[i]])
		right := firstStart(grouped[traceIDs[j]])
		if left.Equal(right) {
			return traceIDs[i] < traceIDs[j]
		}
		return left.Before(right)
	})

	rendered := make([]string, 0, len(traceIDs))
	for _, traceID := range traceIDs {
		out := RenderTrace(grouped[traceID])
		if out != "" {
			rendered = append(rendered, out)
		}
	}

	return strings.Join(rendered, "\n\n")
}

// RenderTrace renders one trace as a CLI-friendly timeline.
func RenderTrace(spans []SpanData) string {
	if len(spans) == 0 {
		return ""
	}

	layout := buildTreeLayout(spans)
	if len(layout) == 0 {
		return ""
	}

	return renderTrace(spans, layout)
}

func buildTreeLayout(spans []SpanData) []treeNode {
	ordered := append([]SpanData(nil), spans...)
	sortSpans(ordered)

	byID := make(map[string]SpanData, len(ordered))
	children := make(map[string][]SpanData)
	roots := make([]SpanData, 0, len(ordered))

	for _, span := range ordered {
		byID[span.ID] = span
	}

	for _, span := range ordered {
		if span.ParentID == "" || span.Root {
			roots = append(roots, span)
			continue
		}
		if _, ok := byID[span.ParentID]; !ok {
			roots = append(roots, span)
			continue
		}
		children[span.ParentID] = append(children[span.ParentID], span)
	}

	for spanID := range children {
		sortSpans(children[spanID])
	}
	sortSpans(roots)

	var layout []treeNode
	for _, root := range roots {
		layout = append(layout, treeNode{spanID: root.ID, name: root.Name})
		kids := children[root.ID]
		for i, child := range kids {
			branch := "├── "
			nextPrefix := "│   "
			if i == len(kids)-1 {
				branch = "└── "
				nextPrefix = "    "
			}
			appendTreeNode(&layout, child, children, "", branch, nextPrefix)
		}
	}

	return layout
}

func appendTreeNode(layout *[]treeNode, span SpanData, children map[string][]SpanData, prefix string, branch string, nextPrefix string) {
	*layout = append(*layout, treeNode{
		spanID: span.ID,
		name:   span.Name,
		prefix: prefix + branch,
	})

	kids := children[span.ID]
	for i, child := range kids {
		childBranch := "├── "
		childPrefix := prefix + nextPrefix
		childNextPrefix := "│   "
		if i == len(kids)-1 {
			childBranch = "└── "
			childNextPrefix = "    "
		}
		appendTreeNode(layout, child, children, childPrefix, childBranch, childNextPrefix)
	}
}

func renderTrace(spans []SpanData, layout []treeNode) string {
	byID := make(map[string]SpanData, len(spans))
	for _, span := range spans {
		byID[span.ID] = span
	}

	var globalStart, globalEnd time.Time
	var traceID string
	for _, span := range spans {
		if globalStart.IsZero() || span.StartTime.Before(globalStart) {
			globalStart = span.StartTime
		}
		if globalEnd.IsZero() || span.EndTime.After(globalEnd) {
			globalEnd = span.EndTime
		}
		if span.Root {
			traceID = span.TraceID
		}
		if traceID == "" {
			traceID = span.TraceID
		}
		if traceID == "" {
			traceID = span.ID
		}
	}

	totalDur := globalEnd.Sub(globalStart)
	scaleDur := totalDur
	if scaleDur <= 0 {
		scaleDur = time.Millisecond
	}
	totalMs := durMs(totalDur)

	maxLabel := 0
	for _, node := range layout {
		if width := len(node.prefix) + len(node.name); width > maxLabel {
			maxLabel = width
		}
	}

	const barWidth = 50
	const idWidth = 8

	var b strings.Builder
	var legend []legendEntry
	keyIdx := 0

	fmt.Fprintf(&b, "═══ TRACE %s ═══  %.1fms\n\n", traceID, totalMs)

	endLabel := fmt.Sprintf("%.1fms", totalMs)
	fmt.Fprintf(&b, "%*s0ms%*s%s\n",
		idWidth+2+maxLabel+2, "",
		barWidth-3-len(endLabel), "", endLabel)

	for _, node := range layout {
		span, ok := byID[node.spanID]
		if !ok {
			continue
		}

		startCol := scaledColumn(span.StartTime.Sub(globalStart), scaleDur, barWidth)
		endCol := scaledColumn(span.EndTime.Sub(globalStart), scaleDur, barWidth)
		if endCol <= startCol {
			endCol = startCol + 1
		}
		if endCol > barWidth {
			endCol = barWidth
		}

		bar := make([]rune, barWidth)
		for i := range bar {
			if i >= startCol && i < endCol {
				bar[i] = '─'
				continue
			}
			bar[i] = '·'
		}

		markers := spanMarkers(span)
		colToLegendIdx := map[int]int{}
		for i, marker := range markers {
			col := markerColumn(i, len(markers), startCol, endCol)
			if idx, exists := colToLegendIdx[col]; exists {
				legend[idx].items = append(legend[idx].items, marker)
				continue
			}

			if keyIdx >= len(legendKeys) {
				break
			}

			key := string(legendKeys[keyIdx])
			keyIdx++
			bar[col] = rune(key[0])
			colToLegendIdx[col] = len(legend)
			legend = append(legend, legendEntry{key: key, items: []markerData{marker}})
		}

		label := node.prefix + span.Name
		dur := durMs(span.EndTime.Sub(span.StartTime))
		attrStr := fmtAttrList(span.Attrs)

		fmt.Fprintf(&b, "%-*s  %-*s  %s %5.1fms", idWidth, span.ID, maxLabel, label, string(bar), dur)
		if attrStr != "" {
			fmt.Fprintf(&b, "  %s", attrStr)
		}
		b.WriteByte('\n')
	}

	if len(legend) > 0 {
		b.WriteByte('\n')

		var lines []string
		for _, entry := range legend {
			for i, item := range entry.items {
				prefix := entry.key
				if i > 0 {
					prefix = " "
				}
				lines = append(lines, fmt.Sprintf("%s  %s %s", prefix, kindSym(item.kind), item.desc))
			}
		}

		mid := (len(lines) + 1) / 2
		for i := 0; i < mid; i++ {
			if i+mid < len(lines) {
				fmt.Fprintf(&b, "%-40s %s\n", lines[i], lines[i+mid])
				continue
			}
			fmt.Fprintf(&b, "%s\n", lines[i])
		}
		fmt.Fprintf(&b, "\n* event  ! error\n")
	}

	return b.String()
}

func spanMarkers(span SpanData) []markerData {
	markers := make([]markerData, 0, len(span.Events)+len(span.Errors))
	for _, event := range span.Events {
		markers = append(markers, markerData{
			kind: eventMarker,
			desc: eventDesc(event),
		})
	}
	for _, err := range span.Errors {
		markers = append(markers, markerData{
			kind: errorMarker,
			desc: errorDesc(err),
		})
	}
	return markers
}

func markerColumn(idx, total, startCol, endCol int) int {
	lastCol := endCol - 1
	if lastCol < startCol {
		return startCol
	}
	if total <= 1 {
		return startCol + (lastCol-startCol)/2
	}

	spanWidth := lastCol - startCol + 1
	col := startCol + ((idx + 1) * spanWidth / (total + 1))
	if col > lastCol {
		return lastCol
	}
	return col
}

func sortSpans(spans []SpanData) {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].StartTime.Equal(spans[j].StartTime) {
			if spans[i].Name == spans[j].Name {
				return spans[i].ID < spans[j].ID
			}
			return spans[i].Name < spans[j].Name
		}
		return spans[i].StartTime.Before(spans[j].StartTime)
	})
}

func firstStart(spans []SpanData) time.Time {
	if len(spans) == 0 {
		return time.Time{}
	}
	start := spans[0].StartTime
	for _, span := range spans[1:] {
		if span.StartTime.Before(start) {
			start = span.StartTime
		}
	}
	return start
}

func scaledColumn(offset time.Duration, total time.Duration, width int) int {
	if total <= 0 || width <= 0 {
		return 0
	}
	if offset <= 0 {
		return 0
	}
	col := int(float64(offset) / float64(total) * float64(width))
	if col < 0 {
		return 0
	}
	if col > width {
		return width
	}
	return col
}

func kindSym(kind markerKind) string {
	switch kind {
	case eventMarker:
		return "*"
	case errorMarker:
		return "!"
	default:
		return "?"
	}
}

func eventDesc(event EventData) string {
	if len(event.Attrs) > 0 {
		return event.Name + "  " + fmtAttrList(event.Attrs)
	}
	return event.Name
}

func errorDesc(err ErrorData) string {
	msg := errorMessage(err.Err)
	if msg == "" {
		msg = fmtAttrList(err.Attrs)
		return msg
	}
	if len(err.Attrs) > 0 {
		msg += "  " + fmtAttrList(err.Attrs)
	}
	return msg
}

func fmtAttrList(attrs []slog.Attr) string {
	if len(attrs) == 0 {
		return ""
	}
	parts := make([]string, len(attrs))
	for i, attr := range attrs {
		parts[i] = fmt.Sprintf("%s=%s", attr.Key, attr.Value)
	}
	return strings.Join(parts, "  ")
}

func durMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
