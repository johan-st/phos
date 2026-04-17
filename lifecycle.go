package phos

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

const shutdownTimeoutEvent = "phos.Shutdown timeout reached"

var (
	drainingState atomic.Bool
	closedState   atomic.Bool
	lifecycleGen  atomic.Uint64

	rootMu    sync.Mutex
	rootSpans = map[*Span]struct{}{}

	closedSignalMu   sync.Mutex
	closedSignal     = make(chan struct{})
	closedSignalOnce sync.Once

	rejectedSpanSignalMu     sync.Mutex
	rejectedSpanSignalWriter io.Writer = os.Stderr
)

// DrainAndClose begins shutdown admission control without blocking.
//
// Once draining has started, new root spans are rejected while child spans on
// still-open local parents are allowed. If ctx is canceled before all open
// roots end naturally, Phos closes the remaining trees bottom-up and records
// the event "phos.Shutdown timeout reached" on affected spans.
func DrainAndClose(ctx context.Context) {
	ctx = normalizeContext(ctx)
	generation := lifecycleGen.Load()
	drainingState.Store(true)

	rootMu.Lock()
	maybeFinalizeClosedLocked()
	rootMu.Unlock()

	if done := ctx.Done(); done != nil {
		go func(done <-chan struct{}) {
			<-done
			beginClose(generation)
		}(done)
	}
}

// WaitForClosed blocks until Phos reaches the closed state.
func WaitForClosed() {
	closedSignalMu.Lock()
	ch := closedSignal
	closedSignalMu.Unlock()
	<-ch
}

func beginClose(generation uint64) {
	if lifecycleGen.Load() != generation {
		return
	}
	closedState.Store(true)

	for _, root := range snapshotRootSpans() {
		root.closeTree(shutdownTimeoutEvent)
	}

	rootMu.Lock()
	maybeFinalizeClosedLocked()
	rootMu.Unlock()
}

func registerRootSpan(span *Span) bool {
	rootMu.Lock()
	defer rootMu.Unlock()
	if drainingState.Load() || closedState.Load() {
		return false
	}
	rootSpans[span] = struct{}{}
	return true
}

func unregisterRootSpan(span *Span) {
	rootMu.Lock()
	delete(rootSpans, span)
	maybeFinalizeClosedLocked()
	rootMu.Unlock()
}

func snapshotRootSpans() []*Span {
	rootMu.Lock()
	defer rootMu.Unlock()

	roots := make([]*Span, 0, len(rootSpans))
	for root := range rootSpans {
		roots = append(roots, root)
	}
	return roots
}

func maybeFinalizeClosedLocked() {
	if !drainingState.Load() {
		return
	}
	if len(rootSpans) != 0 {
		return
	}

	closedState.Store(true)
	closedSignalOnce.Do(func() {
		closedSignalMu.Lock()
		close(closedSignal)
		closedSignalMu.Unlock()
	})
}

func signalRejectedSpan(action, reason, name string) {
	rejectedSpanSignalMu.Lock()
	writer := rejectedSpanSignalWriter
	rejectedSpanSignalMu.Unlock()
	if writer == nil {
		return
	}

	if name == "" {
		_, _ = fmt.Fprintf(writer, "phos: %s on rejected span (%s)\n", action, reason)
		return
	}
	_, _ = fmt.Fprintf(writer, "phos: %s on rejected span %q (%s)\n", action, name, reason)
}

func resetLifecycleForTesting() {
	lifecycleGen.Add(1)
	drainingState.Store(false)
	closedState.Store(false)

	rootMu.Lock()
	rootSpans = map[*Span]struct{}{}
	rootMu.Unlock()

	closedSignalMu.Lock()
	closedSignal = make(chan struct{})
	closedSignalMu.Unlock()
	closedSignalOnce = sync.Once{}

	rejectedSpanSignalMu.Lock()
	rejectedSpanSignalWriter = os.Stderr
	rejectedSpanSignalMu.Unlock()
}
