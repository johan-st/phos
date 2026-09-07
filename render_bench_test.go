package phos

import (
	"archive/tar"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const alibabaCallGraphArchive = "benchdata/alibaba-ms-traces-2021/MSCallGraph/MSCallGraph_0.tar.gz"

var benchmarkRenderedTrace string

func BenchmarkRenderTrace(b *testing.B) {
	for _, spanCount := range []int{1, 16, 128, 1024} {
		spans := benchmarkSnapshots("trace", 0, spanCount)

		b.Run(strconv.Itoa(spanCount), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				benchmarkRenderedTrace = RenderTrace(spans)
			}
		})
	}
}

func BenchmarkRenderTraces(b *testing.B) {
	const (
		traceCount = 16
		spanCount  = 64
	)

	spans := make([]Snapshot, 0, traceCount*spanCount)
	for trace := range traceCount {
		spans = append(spans, benchmarkSnapshots(fmt.Sprintf("trace-%02d", trace), trace*spanCount, spanCount)...)
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkRenderedTrace = RenderTraces(spans)
	}
}

func BenchmarkRenderTraceAlibaba(b *testing.B) {
	spans := loadAlibabaBenchmarkTrace(b, 256)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		benchmarkRenderedTrace = RenderTrace(spans)
	}
}

func benchmarkSnapshots(traceID string, idOffset int, spanCount int) []Snapshot {
	start := time.Unix(0, 0)
	spans := make([]Snapshot, spanCount)

	for i := range spanCount {
		id := fmt.Sprintf("%016x", idOffset+i+1)
		parentID := ""
		if i > 0 {
			parentID = fmt.Sprintf("%016x", idOffset+(i-1)/2+1)
		}

		spanStart := start.Add(time.Duration(i) * time.Millisecond)
		spanEnd := start.Add(time.Duration(2*spanCount-i) * time.Millisecond)
		spans[i] = Snapshot{
			ID:        id,
			Name:      fmt.Sprintf("operation.%d", i),
			ParentID:  parentID,
			TraceID:   traceID,
			TimeStart: spanStart,
			TimeEnd:   spanEnd,
			Attrs: []slog.Attr{
				slog.String("service", "benchmark"),
			},
		}
		if i%8 == 0 {
			spans[i].Events = []SnapshotEvent{{
				Time: spanStart,
				Name: "checkpoint",
			}}
		}
	}

	return spans
}

// Alibaba records calls rather than Phos spans. This benchmark-only mapping
// uses each unique non-negative RPC record's measured timing and rpcid tree.
func loadAlibabaBenchmarkTrace(b *testing.B, limit int) []Snapshot {
	b.Helper()

	file, err := os.Open(alibabaCallGraphArchive)
	if errors.Is(err, os.ErrNotExist) {
		b.Skipf("Alibaba corpus not found at %s", alibabaCallGraphArchive)
	}
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		b.Fatal(err)
	}
	defer gz.Close()

	tarReader := tar.NewReader(gz)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			b.Fatal("Alibaba archive contains no CSV file")
		}
		if nextErr != nil {
			b.Fatal(nextErr)
		}
		if strings.HasSuffix(header.Name, ".csv") {
			break
		}
	}

	reader := csv.NewReader(tarReader)
	header, err := reader.Read()
	if err != nil {
		b.Fatal(err)
	}
	if len(header) != 9 || header[1] != "traceid" || header[2] != "timestamp" ||
		header[3] != "rpcid" || header[5] != "rpctype" || header[8] != "rt" {
		b.Fatalf("unexpected Alibaba CSV header: %q", header)
	}

	var traceID string
	seenRPCIDs := make(map[string]struct{}, limit)
	spans := make([]Snapshot, 0, limit)
	for len(spans) < limit {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			b.Fatal(readErr)
		}
		if len(record) != 9 {
			b.Fatalf("Alibaba row has %d columns, want 9", len(record))
		}
		if traceID == "" {
			traceID = record[1]
		}
		if record[1] != traceID {
			continue
		}

		rpcID := record[3]
		if _, exists := seenRPCIDs[rpcID]; exists {
			continue
		}
		timestampMS, timestampErr := strconv.ParseInt(record[2], 10, 64)
		if timestampErr != nil {
			b.Fatal(timestampErr)
		}
		durationMS, durationErr := strconv.ParseInt(record[8], 10, 64)
		if durationErr != nil {
			b.Fatal(durationErr)
		}
		if durationMS < 0 {
			continue
		}

		seenRPCIDs[rpcID] = struct{}{}
		spanStart := time.Unix(0, 0).Add(time.Duration(timestampMS) * time.Millisecond)
		spans = append(spans, Snapshot{
			ID:        rpcID,
			Name:      record[5],
			ParentID:  parentRPCID(rpcID),
			TraceID:   traceID,
			TimeStart: spanStart,
			TimeEnd:   spanStart.Add(time.Duration(durationMS) * time.Millisecond),
		})
	}

	if len(spans) < limit {
		b.Fatalf("Alibaba trace contains %d usable calls, want %d", len(spans), limit)
	}
	return spans
}

func parentRPCID(rpcID string) string {
	separator := strings.LastIndexByte(rpcID, '.')
	if separator < 0 {
		return ""
	}
	return rpcID[:separator]
}
