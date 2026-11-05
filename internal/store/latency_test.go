package store

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"
)

// TestSearchLatencyOnRealData measures search latency on a real database. It only runs when
// M3_BENCH_DB points at a published m3.sqlite:
//
//	M3_BENCH_DB=/path/to/m3.sqlite go test -run TestSearchLatencyOnRealData -v ./internal/store
func TestSearchLatencyOnRealData(t *testing.T) {
	path := os.Getenv("M3_BENCH_DB")
	if path == "" {
		t.Skip("set M3_BENCH_DB to measure")
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	var rows int
	_ = r.db.QueryRow("SELECT count(*) FROM programs").Scan(&rows)
	t.Logf("database: %d programs", rows)

	terms := []string{"", "film", "magyar", "béke", "BEKE", "vadász", "színház", "történelem", "zzzzzz", "a kert", "1956", "Nagy", "M3-5994", "dokumentum", "sorozat"}
	var all []time.Duration
	for _, term := range terms {
		var d []time.Duration
		var total int
		for i := 0; i < 50; i++ {
			start := time.Now()
			res, err := r.Search(ctx, term, 10, 0)
			if err != nil && err != ErrTermTooShort {
				t.Fatalf("%q: %v", term, err)
			}
			d = append(d, time.Since(start))
			total = res.Total
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t.Logf("%-14q matches=%-5d p50=%-9s p95=%-9s max=%s", term, total, d[len(d)/2], d[len(d)*95/100], d[len(d)-1])
		all = append(all, d...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	t.Logf("ALL: p50=%s p95=%s p99=%s max=%s", all[len(all)/2], all[len(all)*95/100], all[len(all)*99/100], all[len(all)-1])
}
