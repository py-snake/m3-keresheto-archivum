// Package ingest collects programs and cover images from the upstream into the database.
//
// Every run works on a private copy of the database and publishes it atomically (store.Publisher).
// Anything the upstream sends is validated; a run that sees too many invalid items stops
// without publishing anything (circuit breaker).
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	"github.com/py-snake/m3-keresheto-archivum/internal/upstream"
)

// Tunables with safe defaults.
const (
	defaultInvalidRatio = 0.05
	minItemsForRatio    = 20
	defaultMaxItems     = 3000 // upstream item lookups per weekly run
	maxImageFailStreak  = 10   // consecutive transport failures that stop the image step
)

// Ingestor runs the ingest jobs.
type Ingestor struct {
	// Dir is the data directory.
	Dir string
	// Keep is the number of rotated database backups.
	Keep int
	// Up is the upstream client.
	Up *upstream.Client
	// Log receives structured progress messages.
	Log *slog.Logger
	// ImagesPerRun caps the cover image downloads of one run (0 = no images).
	ImagesPerRun int
	// ImageMaxWidth scales cover images down (0 = keep the size).
	ImageMaxWidth int
	// InvalidRatio is the share of invalid upstream items that aborts a run. Default 5%.
	InvalidRatio float64
	// MaxItems caps the item lookups of one weekly run. Default 3000.
	MaxItems int
	// RecheckPerRun is how many stored programs Recheck re-queries (0 = disabled).
	RecheckPerRun int
	// RecheckDelay is the pause between two recheck requests.
	RecheckDelay time.Duration
	// Now is the clock (tests).
	Now func() time.Time
}

// Result describes a finished run.
type Result struct {
	Summary store.Summary
	// New is the number of programs added.
	New int
	// Images is the number of cover images stored.
	Images int
	// Errors counts non-fatal problems (failed requests); the run still published what it got.
	Errors int
	// Observed / Changed count availability observations and how many changed the stored flag.
	Observed, Changed int
	// Rechecked / Gone count the programs Recheck re-queried and the ones the upstream no longer knows.
	Rechecked, Gone int
	// BudgetStopped is set when the request budget ended the work early (not an error).
	BudgetStopped bool
}

func (g *Ingestor) publisher() *store.Publisher {
	return &store.Publisher{Dir: g.Dir, Keep: g.Keep, Now: g.Now}
}

func (g *Ingestor) ratio() float64 {
	if g.InvalidRatio > 0 {
		return g.InvalidRatio
	}
	return defaultInvalidRatio
}

// breaker aborts a run when the upstream's data looks broken as a whole.
func (g *Ingestor) breaker(source string, rep upstream.Report) error {
	if rep.Total == 0 {
		return nil
	}
	tooMany := rep.Total >= minItemsForRatio && float64(rep.Invalid)/float64(rep.Total) > g.ratio()
	allBad := rep.Invalid == rep.Total
	if tooMany || allBad {
		return fmt.Errorf("%s: %d of %d upstream items are invalid (e.g. %v): refusing to publish",
			source, rep.Invalid, rep.Total, rep.Samples)
	}
	if rep.Invalid > 0 || rep.Truncated > 0 {
		g.Log.Warn("some upstream items were dropped or truncated", "source", source,
			"invalid", rep.Invalid, "truncated", rep.Truncated, "examples", rep.Samples)
	}
	return nil
}

// Daily ingests today's program and the program guide.
func (g *Ingestor) Daily(ctx context.Context) (Result, error) {
	var res Result
	sum, err := g.publisher().Run(ctx, func(ctx context.Context, w *store.Writer) error {
		var items []json.RawMessage
		var failed []error
		collect := func(name string, fetch func(context.Context) ([]json.RawMessage, error)) {
			list, err := fetch(ctx)
			if err != nil {
				g.Log.Error("fetching failed", "source", name, "err", err)
				failed = append(failed, fmt.Errorf("%s: %w", name, err))
				return
			}
			g.Log.Info("fetched", "source", name, "items", len(list))
			items = append(items, list...)
		}
		collect("daily_program", g.Up.DailyProgram)
		collect("program_guide", g.Up.ProgramGuide)
		if len(failed) == 2 {
			return fmt.Errorf("nothing could be fetched: %w", errors.Join(failed...))
		}
		res.Errors += len(failed)

		return g.store(ctx, w, "daily", items, &res)
	})
	res.Summary = sum
	return res, g.finish(res, err)
}

// Weekly ingests the open-archive collections (including every episode of their series).
func (g *Ingestor) Weekly(ctx context.Context) (Result, error) {
	var res Result
	sum, err := g.publisher().Run(ctx, func(ctx context.Context, w *store.Writer) error {
		colls, err := g.Up.CollectionIDs(ctx)
		if err != nil {
			return fmt.Errorf("collection list: %w", err)
		}

		var docs []upstream.CollectionDoc
		for _, c := range colls {
			list, err := g.Up.Collection(ctx, c)
			if errors.Is(err, upstream.ErrBudget) {
				return err
			}
			if err != nil {
				res.Errors++
				g.Log.Error("collection failed", "err", err)
				continue
			}
			docs = append(docs, list...)
		}
		if len(docs) == 0 {
			return errors.New("no collection items could be fetched")
		}

		var ids []string
		seenSeries := map[string]bool{}
		for _, d := range docs {
			switch {
			case d.SeriesID != "":
				if seenSeries[d.SeriesID] {
					continue
				}
				seenSeries[d.SeriesID] = true
				sids, err := g.Up.SeriesProgramIDs(ctx, d.SeriesID)
				if errors.Is(err, upstream.ErrBudget) {
					return err
				}
				if err != nil {
					res.Errors++
					g.Log.Error("series failed", "err", err)
				}
				ids = append(ids, sids...)
			case d.ID != "":
				ids = append(ids, d.ID)
			}
		}

		missing, err := w.MissingIDs(ctx, ids)
		if err != nil {
			return err
		}
		g.Log.Info("weekly scan", "collections", len(colls), "series", len(seenSeries), "program_ids", len(ids), "missing", len(missing))

		raw, errCount := g.lookup(ctx, missing)
		res.Errors += errCount
		return g.store(ctx, w, "weekly", raw, &res)
	})
	res.Summary = sum
	return res, g.finish(res, err)
}

// Items ingests specific programs by id (an administrator tool: `m3 ingest item M3-...`).
func (g *Ingestor) Items(ctx context.Context, ids []string) (Result, error) {
	var res Result
	for _, id := range ids {
		if !progid.Valid(id) {
			return res, fmt.Errorf("malformed program id %q", id)
		}
	}
	sum, err := g.publisher().Run(ctx, func(ctx context.Context, w *store.Writer) error {
		missing, err := w.MissingIDs(ctx, ids)
		if err != nil {
			return err
		}
		raw, errCount := g.lookup(ctx, missing)
		res.Errors += errCount
		return g.store(ctx, w, "item", raw, &res)
	})
	res.Summary = sum
	return res, g.finish(res, err)
}

// Images downloads pending cover images only (backfill).
func (g *Ingestor) Images(ctx context.Context) (Result, error) {
	var res Result
	sum, err := g.publisher().Run(ctx, func(ctx context.Context, w *store.Writer) error {
		return g.fetchImages(ctx, w, &res)
	})
	res.Summary = sum
	return res, g.finish(res, err)
}

// lookup fetches the details of the given ids, one request each, at most MaxItems.
func (g *Ingestor) lookup(ctx context.Context, ids []string) ([]json.RawMessage, int) {
	limit := g.MaxItems
	if limit <= 0 {
		limit = defaultMaxItems
	}
	if len(ids) > limit {
		g.Log.Warn("item lookup budget reached; the rest is left for the next run", "wanted", len(ids), "budget", limit)
		ids = ids[:limit]
	}
	var out []json.RawMessage
	errCount := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		raw, err := g.Up.Item(ctx, id)
		switch {
		case errors.Is(err, upstream.ErrBudget):
			g.Log.Warn("request budget reached; the rest is left for the next run")
			return out, errCount
		case errors.Is(err, upstream.ErrNotFound):
			g.Log.Info("the upstream has no such item", "program_id", id)
		case err != nil:
			errCount++
			g.Log.Error("item lookup failed", "program_id", id, "err", err)
		default:
			out = append(out, raw)
		}
	}
	return out, errCount
}

// store parses and inserts items, then runs the image step.
func (g *Ingestor) store(ctx context.Context, w *store.Writer, source string, items []json.RawMessage, res *Result) error {
	progs, rep := upstream.ParsePrograms(items)
	if err := g.breaker(source, rep); err != nil {
		return err
	}
	n, err := w.InsertPrograms(ctx, dedupe(progs))
	if err != nil {
		return err
	}
	res.New += n
	obs, chg, err := w.RecordAvailability(ctx, dedupeLast(progs))
	if err != nil {
		return err
	}
	res.Observed += obs
	res.Changed += chg
	g.Log.Info("stored", "source", source, "parsed", len(progs), "new", n, "availability_observed", obs, "availability_changed", chg)
	return g.fetchImages(ctx, w, res)
}

// dedupeLast keeps the last occurrence of every program (the most recent observation wins).
func dedupeLast(ps []model.Program) []model.Program {
	last := make(map[string]int, len(ps))
	for i, p := range ps {
		last[p.ProgramID] = i
	}
	out := make([]model.Program, 0, len(last))
	for i, p := range ps {
		if last[p.ProgramID] == i {
			out = append(out, p)
		}
	}
	return out
}

func dedupe(ps []model.Program) []model.Program {
	seen := make(map[string]bool, len(ps))
	out := ps[:0:0]
	for _, p := range ps {
		if !seen[p.ProgramID] {
			seen[p.ProgramID] = true
			out = append(out, p)
		}
	}
	return out
}

// fetchImages downloads up to ImagesPerRun pending cover images. Image problems never fail a run.
func (g *Ingestor) fetchImages(ctx context.Context, w *store.Writer, res *Result) error {
	if g.ImagesPerRun <= 0 {
		return nil
	}
	ids, err := w.PendingImages(ctx, g.ImagesPerRun)
	if err != nil {
		return err
	}
	dir := filepath.Join(g.Dir, "images")
	streak := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		data, err := g.Up.Image(ctx, id)
		if errors.Is(err, upstream.ErrBudget) {
			g.Log.Warn("request budget reached; the remaining images are left for the next run")
			res.BudgetStopped = true
			return nil
		}
		if err != nil {
			res.Errors++
			streak++
			g.Log.Warn("image download failed", "program_id", id, "err", err)
			if streak >= maxImageFailStreak {
				g.Log.Error("too many consecutive image failures; stopping the image step")
				return nil
			}
			continue
		}
		streak = 0

		clean, err := images.Sanitize(data, g.ImageMaxWidth)
		switch {
		case errors.Is(err, images.ErrNotJPEG), errors.Is(err, images.ErrTooLarge):
			// the upstream answers HTTP 200 with an HTML page when it has no image
			g.Log.Info("no usable image upstream", "program_id", id, "reason", err)
			if err := w.SetImageState(ctx, id, model.ImageMissing); err != nil {
				return err
			}
		case err != nil:
			res.Errors++
			g.Log.Warn("image processing failed", "program_id", id, "err", err)
		default:
			if err := images.Save(dir, id, clean); err != nil {
				return fmt.Errorf("saving the image of %s: %w", id, err)
			}
			if err := w.SetImageState(ctx, id, model.ImageStored); err != nil {
				return err
			}
			res.Images++
		}
	}
	return nil
}

// finish turns a published-with-problems run into an error so that schedulers notice.
func (g *Ingestor) finish(res Result, err error) error {
	if err != nil {
		return err
	}
	g.Log.Info("run finished", "published", res.Summary.Published, "programs_before", res.Summary.Before,
		"programs_after", res.Summary.After, "new", res.New, "images", res.Images, "errors", res.Errors,
		"availability_observed", res.Observed, "availability_changed", res.Changed, "requests", g.Up.Requests())
	if res.Errors > 0 {
		return fmt.Errorf("finished with %d non-fatal error(s); see the log", res.Errors)
	}
	return nil
}

// Migrate upgrades the database schema to the current version and publishes it. It sends no
// request to the upstream. Running any ingest job does the same; this is the explicit form.
func (g *Ingestor) Migrate(ctx context.Context) (Result, error) {
	var res Result
	sum, err := g.publisher().Run(ctx, func(context.Context, *store.Writer) error { return nil })
	res.Summary = sum
	return res, g.finish(res, err)
}

// Recheck re-queries the availability of the stored programs whose last observation is the oldest.
//
// It is the only job that sends requests *only* to refresh data, so it is deliberately gentle:
// it does nothing unless RecheckPerRun is set, it sends at most that many requests, with a long
// pause between them, and it stops at the request budget.
func (g *Ingestor) Recheck(ctx context.Context) (Result, error) {
	var res Result
	if g.RecheckPerRun <= 0 {
		g.Log.Warn("recheck is disabled: set M3_RECHECK_PER_RUN (a small number such as 30) to enable it. " +
			"Availability is refreshed anyway by the requests of the daily and weekly jobs")
		return res, nil
	}
	sum, err := g.publisher().Run(ctx, func(ctx context.Context, w *store.Writer) error {
		ids, err := w.StalestForRecheck(ctx, g.RecheckPerRun)
		if err != nil {
			return err
		}
		var raws []json.RawMessage
		var fetched []string
	loop:
		for i, id := range ids {
			if i > 0 {
				if err := sleep(ctx, g.RecheckDelay); err != nil {
					return err
				}
			}
			raw, err := g.Up.Item(ctx, id)
			switch {
			case errors.Is(err, upstream.ErrBudget):
				g.Log.Warn("request budget reached; stopping the recheck")
				res.BudgetStopped = true
				break loop
			case errors.Is(err, upstream.ErrNotFound):
				res.Gone++
				g.Log.Info("the upstream no longer knows this program", "program_id", id)
				if err := w.MarkGone(ctx, id); err != nil {
					return err
				}
			case err != nil:
				res.Errors++
				g.Log.Error("recheck failed", "program_id", id, "err", err)
			default:
				res.Rechecked++
				raws = append(raws, raw)
				fetched = append(fetched, id)
			}
		}
		// an item without a playable flag must still count as checked, or it would be re-queried every run
		if err := w.TouchChecked(ctx, fetched); err != nil {
			return err
		}
		progs, rep := upstream.ParsePrograms(raws)
		if err := g.breaker("recheck", rep); err != nil {
			return err
		}
		res.Observed, res.Changed, err = w.RecordAvailability(ctx, dedupeLast(progs))
		return err
	})
	res.Summary = sum
	return res, g.finish(res, err)
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
