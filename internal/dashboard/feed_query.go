package dashboard

// Data selection only; presentation, trees, pagination and formatting are Gleam.
import (
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/johan-st/phos"
)

type feedFilters struct {
	Query, ServiceFilter string
	MinDurationMs        int
	FailedOnly           bool
}

type feedGroup struct {
	TraceID, Name      string
	Spans              []phos.Snapshot
	StartedAt, EndedAt time.Time
	Failed             bool
}

func feedGroups(spans []phos.Snapshot) []feedGroup {
	grouped := map[string]map[string]phos.Snapshot{}
	for _, s := range spans {
		id := s.TraceID
		if id == "" {
			id = s.ID
		}
		if grouped[id] == nil {
			grouped[id] = map[string]phos.Snapshot{}
		}
		grouped[id][s.ID] = s
	}
	groups := make([]feedGroup, 0, len(grouped))
	for id, byID := range grouped {
		items := make([]phos.Snapshot, 0, len(byID))
		for _, s := range byID {
			items = append(items, s)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].TimeStart.Equal(items[j].TimeStart) {
				return items[i].ID < items[j].ID
			}
			return items[i].TimeStart.Before(items[j].TimeStart)
		})
		g := feedGroup{TraceID: id, Name: items[0].Name, Spans: items, StartedAt: items[0].TimeStart, EndedAt: items[0].TimeEnd}
		rootFound := false
		for _, s := range items {
			_, parentExists := byID[s.ParentID]
			if !rootFound && (s.ParentID == "" || !parentExists) {
				g.Name = s.Name
				rootFound = true
			}
			if s.TimeStart.Before(g.StartedAt) {
				g.StartedAt = s.TimeStart
			}
			if s.TimeEnd.After(g.EndedAt) {
				g.EndedAt = s.TimeEnd
			}
			g.Failed = g.Failed || feedFailed(s)
		}
		groups = append(groups, g)
	}
	return groups
}

func feedFailed(s phos.Snapshot) bool { return s.Failed || len(s.Errors) > 0 }
func feedError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func feedEventTime(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

func feedMatches(g feedGroup, f feedFilters) bool {
	service, _, _ := strings.Cut(g.Name, ":")
	if !strings.Contains(g.Name, ":") {
		service = ""
	}
	if f.FailedOnly && !g.Failed || f.ServiceFilter != "" && !strings.EqualFold(service, f.ServiceFilter) || g.EndedAt.Sub(g.StartedAt).Milliseconds() < int64(f.MinDurationMs) {
		return false
	}
	q := strings.ToLower(f.Query)
	if q == "" {
		return true
	}
	contains := func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	var attrsMatch func([]slog.Attr) bool
	attrsMatch = func(attrs []slog.Attr) bool {
		for _, a := range attrs {
			v := a.Value.Resolve()
			if contains(a.Key) || contains(v.String()) {
				return true
			}
			if v.Kind() == slog.KindGroup && attrsMatch(v.Group()) {
				return true
			}
		}
		return false
	}
	if contains(g.TraceID) {
		return true
	}
	for _, s := range g.Spans {
		if contains(s.ID) || contains(s.Name) || attrsMatch(s.Attrs) {
			return true
		}
		for _, e := range s.Events {
			if contains(e.Name) || attrsMatch(e.Attrs) {
				return true
			}
		}
		for _, e := range s.Errors {
			if contains(feedError(e.Err)) || attrsMatch(e.Attrs) {
				return true
			}
		}
	}
	return false
}
