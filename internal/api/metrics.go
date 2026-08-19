package api

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metrics is what an operator needs to see without reading logs: how many
// solves, how many worked, and how long they took.
//
// Written out in the Prometheus text exposition format, by hand. The format is
// small and stable, and the official client would roughly triple a dependency
// tree that is currently two entries, both of them chromedp.
type Metrics struct {
	mu sync.Mutex

	// solves counts outcomes per vendor. The label space is small and known,
	// so a plain map under a mutex beats anything cleverer.
	solves map[outcome]int64

	// buckets is the cumulative count per upper bound, by index into
	// durationBounds. Cumulative is the format's own convention: each bucket
	// holds everything at or below its bound.
	buckets []int64
	sum     float64
	count   int64
}

// outcome is one labelled counter. Both labels come from closed sets — the
// vendor kinds the registry knows and the Reason constants — so the number of
// series this can produce is bounded by the code rather than by traffic.
type outcome struct {
	kind   string
	result string // "token" or "failed"
	reason Reason
}

// durationBounds are the upper edges of the latency histogram, in seconds.
// Chosen against measured solves: a Turnstile lands near 3s, a picture
// challenge near 12s, a site behind a managed challenge near 25s, and anything
// past 60s has hit the default ceiling rather than finished.
var durationBounds = []float64{1, 2, 5, 10, 15, 20, 30, 45, 60, 90, 120}

// NewMetrics builds an empty set.
func NewMetrics() *Metrics {
	return &Metrics{
		solves:  make(map[outcome]int64),
		buckets: make([]int64, len(durationBounds)),
	}
}

// Observe records one finished solve.
//
// A request turned away because the whole fleet was resting is recorded with a
// zero duration: it is a real outcome an operator needs on the graph, but it
// did not spend any time solving and putting it in the latency histogram would
// drag every quantile toward nothing.
func (m *Metrics) Observe(kind string, took time.Duration, solved bool, reason Reason) {
	if kind == "" {
		kind = "turnstile"
	}
	result := "failed"
	if solved {
		result = "token"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.solves[outcome{kind: kind, result: result, reason: reason}]++

	if reason == ReasonBusy {
		return
	}

	seconds := took.Seconds()
	m.count++
	m.sum += seconds
	for i, bound := range durationBounds {
		if seconds <= bound {
			m.buckets[i]++
		}
	}
}

// Write renders the current values. gauges are the live numbers the registry
// does not own — how much of the fleet is free, how many slots are busy —
// passed in rather than tracked, so there is one source of truth for each.
func (m *Metrics) Write(w io.Writer, gauges map[string]int) {
	m.mu.Lock()
	rows := make([]string, 0, len(m.solves))
	for o, n := range m.solves {
		rows = append(rows, fmt.Sprintf(
			"postern_solves_total{kind=%q,outcome=%q,reason=%q} %d",
			o.kind, o.result, o.reason, n))
	}
	buckets := append([]int64(nil), m.buckets...)
	sum, count := m.sum, m.count
	m.mu.Unlock()

	// Sorted so a diff between two scrapes is readable by a person.
	sort.Strings(rows)

	fmt.Fprintln(w, "# HELP postern_solves_total Solves finished, by vendor, outcome and cause.")
	fmt.Fprintln(w, "# TYPE postern_solves_total counter")
	for _, r := range rows {
		fmt.Fprintln(w, r)
	}

	fmt.Fprintln(w, "# HELP postern_solve_duration_seconds How long a solve took, successful or not.")
	fmt.Fprintln(w, "# TYPE postern_solve_duration_seconds histogram")
	for i, bound := range durationBounds {
		fmt.Fprintf(w, "postern_solve_duration_seconds_bucket{le=%q} %d\n",
			strconv.FormatFloat(bound, 'g', -1, 64), buckets[i])
	}
	// +Inf is required, and equals the total: every observation is at or below
	// an unbounded edge.
	fmt.Fprintf(w, "postern_solve_duration_seconds_bucket{le=\"+Inf\"} %d\n", count)
	fmt.Fprintf(w, "postern_solve_duration_seconds_sum %s\n",
		strconv.FormatFloat(sum, 'f', 3, 64))
	fmt.Fprintf(w, "postern_solve_duration_seconds_count %d\n", count)

	// The conventional shape for build metadata: a gauge that is always 1,
	// carrying the version as a label. It exists so a dashboard can group by
	// version and see a deploy happen.
	fmt.Fprintln(w, "# HELP postern_build_info The version this binary was built as.")
	fmt.Fprintln(w, "# TYPE postern_build_info gauge")
	fmt.Fprintf(w, "postern_build_info{version=%q} 1\n", BuildVersion)

	names := make([]string, 0, len(gauges))
	for n := range gauges {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE %s gauge\n%s %d\n", n, n, gauges[n])
	}
}
