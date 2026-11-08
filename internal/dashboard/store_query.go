package dashboard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/johan-st/phos"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("dashboard_equal_fold", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		a, _ := args[0].(string)
		b, _ := args[1].(string)
		if strings.EqualFold(a, b) {
			return int64(1), nil
		}
		return int64(0), nil
	})
}

// Results are disposable, disk-backed and isolated from the ingestion writer.
// Ordinal cursors page a frozen sorted result with indexed seeks, not OFFSET scans.
type storedResults struct {
	db    *sql.DB
	tx    *sql.Tx
	path  string
	count int
}

func newStoredResults(ctx context.Context, dir string) (*storedResults, error) {
	f, err := os.CreateTemp(dir, "query-*.db")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	db.SetMaxOpenConns(1)
	r := &storedResults{db: db, path: path}
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode=OFF; CREATE TABLE matches(trace TEXT PRIMARY KEY,sort REAL NOT NULL,raw BLOB NOT NULL);CREATE TABLE facets(kind TEXT,value TEXT,n INTEGER,PRIMARY KEY(kind,value));CREATE TABLE pages(ordinal INTEGER PRIMARY KEY,raw BLOB NOT NULL)`)
	if err == nil {
		r.tx, err = db.BeginTx(ctx, nil)
	}
	if err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}
func (r *storedResults) close() {
	if r.tx != nil {
		r.tx.Rollback()
	}
	r.db.Close()
	os.Remove(r.path)
}
func (r *storedResults) add(ctx context.Context, t traceSummary, sort string) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	key := t.Start
	if sort == "slowest" {
		key = t.Duration
	} else if sort == "errors" {
		key = float64(t.Errors)
	}
	_, err = r.tx.ExecContext(ctx, `INSERT INTO matches VALUES(?,?,?)`, t.TraceID, key, raw)
	if err == nil {
		r.count++
	}
	return err
}
func (r *storedResults) facets(ctx context.Context, svc, op string, n int) error {
	if n == 0 {
		return nil
	}
	for kind, value := range map[string]string{"service": svc, "operation": op} {
		if _, err := r.tx.ExecContext(ctx, `INSERT INTO facets VALUES(?,?,?) ON CONFLICT(kind,value) DO UPDATE SET n=n+excluded.n`, kind, value, n); err != nil {
			return err
		}
	}
	return nil
}
func (r *storedResults) finish(ctx context.Context) error {
	if _, err := r.tx.ExecContext(ctx, `INSERT INTO pages SELECT row_number() OVER(ORDER BY sort DESC,trace DESC),raw FROM matches`); err != nil {
		return err
	}
	return r.tx.Commit()
}
func (r *storedResults) page(ctx context.Context, offset int) ([]traceSummary, error) {
	out := []traceSummary{}
	rows, err := r.db.QueryContext(ctx, `SELECT raw FROM pages WHERE ordinal>? ORDER BY ordinal LIMIT ?`, offset, searchPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var t traceSummary
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (r *storedResults) facet(ctx context.Context, kind string) (searchFacet, error) {
	out := searchFacet{Values: []facetValue{}}
	rows, err := r.db.QueryContext(ctx, `SELECT value,n FROM facets WHERE kind=? ORDER BY n DESC,value`, kind)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v facetValue
		if err = rows.Scan(&v.Value, &v.Count); err != nil {
			return out, err
		}
		if v.Value == "" || len(out.Values) >= 8 {
			out.Other += v.Count
		} else {
			out.Values = append(out.Values, v)
		}
	}
	return out, rows.Err()
}
func summaryMatches(t traceSummary, q searchQuery) bool {
	return (q.TraceID == "" || t.TraceID == q.TraceID) && t.Duration >= float64(q.MinDuration) && (q.MaxDuration == 0 || t.Duration <= float64(q.MaxDuration)) && (q.Errors != "with" || t.Errors > 0) && (q.Errors != "without" || t.Errors == 0)
}
func bucketCompatible(q searchQuery) bool {
	return q.TraceID == "" && q.Text == "" && len(q.Attributes) == 0 && q.MinDuration == 0 && q.MaxDuration == 0 && (q.Errors == "" || q.Errors == "all")
}

func (s *dashboardStore) runQuery(ctx context.Context, j *searchJob) {
	w, err := s.Stamp(ctx)
	j.mu.Lock()
	j.stamp = w
	j.mu.Unlock()
	var r *storedResults
	agg := searchAggregates{BucketVersion: 1, DurationBounds: durationBounds, Latency: []latencyBucket{}}
	agg.Buckets, agg.BucketWidth = overviewBuckets(j.query.From, j.query.To)
	hist := newHistogram()
	if err == nil {
		r, err = newStoredResults(ctx, s.resultsDir)
	}
	scanned := 0
	if err == nil {
		if bucketCompatible(j.query) {
			// Candidate IDs deduplicate traces across matching spans and time buckets.
			var rows *sql.Rows
			rows, err = s.db.QueryContext(ctx, `SELECT raw FROM dashboard_traces WHERE lo<=? AND (hi IS NULL OR hi>?) AND trace IN (SELECT trace FROM dashboard_spans WHERE start>=? AND start<? AND lo<=? AND (hi IS NULL OR hi>?) AND (?='' OR dashboard_equal_fold(service,?)) AND (?='' OR operation=?))`, w.MaxSeq, w.MaxSeq, j.query.From, j.query.To, w.MaxSeq, w.MaxSeq, j.query.Service, j.query.Service, j.query.Operation, j.query.Operation)
			if err == nil {
				for rows.Next() {
					var raw []byte
					var t traceSummary
					if err = rows.Scan(&raw); err != nil {
						break
					}
					if err = json.Unmarshal(raw, &t); err != nil {
						break
					}
					if err = r.add(ctx, t, j.query.Sort); err != nil {
						break
					}
					agg.Traces++
					if t.Errors > 0 {
						agg.ErrorTraces++
					}
					scanned++
					if scanned%1000 == 0 {
						j.mu.Lock()
						j.coverage.Scanned = scanned
						j.seq++
						j.mu.Unlock()
					}
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
			}
			if err == nil {
				err = s.projectedOverview(ctx, j.query, w, r, &agg, hist)
			}
		} else {
			stats := make([]bucketAccumulator, len(agg.Buckets))
			_, err = s.Visit(ctx, j.query, w, func(spans []phos.Snapshot) error {
				scanned++
				t := summarize(spans)
				if !summaryMatches(t, j.query) {
					return nil
				}
				matched := false
				for _, p := range spans {
					if !matchSpan(p, j.query) {
						continue
					}
					matched = true
					idx := int(p.TimeStart.UnixMilli()/agg.BucketWidth - j.query.From/agg.BucketWidth)
					agg.Buckets[idx].addSpan(p)
					stats[idx].addSpan(p)
					agg.Spans++
					if feedFailed(p) {
						agg.Errors++
					}
					if !p.TimeEnd.IsZero() && !p.TimeEnd.Before(p.TimeStart) {
						hist.RecordValue(min(max(int64(1), p.TimeEnd.Sub(p.TimeStart).Microseconds()), int64(3600000000000)))
					}
					svc, op := spanNames(p)
					if e := r.facets(ctx, svc, op, 1); e != nil {
						return e
					}
				}
				if !matched {
					return nil
				}
				if e := r.add(ctx, t, j.query.Sort); e != nil {
					return e
				}
				agg.Traces++
				if t.Errors > 0 {
					agg.ErrorTraces++
				}
				if scanned%1000 == 0 {
					j.mu.Lock()
					j.coverage.Scanned = scanned
					j.seq++
					j.mu.Unlock()
				}
				return nil
			})
			finishBucketStats(agg.Buckets, stats)
		}
	}
	if err == nil {
		err = r.finish(ctx)
	}
	if err == nil {
		agg.Services, err = r.facet(ctx, "service")
	}
	if err == nil {
		agg.Operations, err = r.facet(ctx, "operation")
	}
	if err == nil {
		err = s.validate(ctx, w)
	}
	if err == nil {
		err = ctx.Err()
	}
	finishHistogram(&agg, hist)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.disposed {
		if r != nil {
			r.close()
		}
		return
	}
	j.coverage.Running = false
	j.coverage.Scanned = scanned
	j.coverage.Complete = err == nil
	j.seq++
	if err != nil {
		j.coverage.Reason = fmt.Sprintf("Local query incomplete: %v", err)
		// Failed materialisation has no consistent page/aggregate population.
		if r != nil {
			r.close()
		}
		return
	}
	j.result = r
	j.aggregates = agg
	for _, v := range agg.Services.Values {
		j.services[v.Value] = true
	}
	for _, v := range agg.Operations.Values {
		j.operations[v.Value] = true
	}
}

func (j *searchJob) dispose() {
	j.cancel()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.disposed = true
	if j.result != nil {
		j.result.close()
		j.result = nil
	}
}
