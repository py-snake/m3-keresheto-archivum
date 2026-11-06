package store

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Source keeps a Reader on the currently published database and swaps it when the ingest
// publishes a new file (a new inode appears at the same path).
type Source struct {
	path     string
	log      *slog.Logger
	interval time.Duration
	grace    time.Duration

	cur  atomic.Pointer[Reader]
	last os.FileInfo
}

// NewSource creates a Source for dir/m3.sqlite. Call Refresh once, then Run.
func NewSource(dir string, log *slog.Logger) *Source {
	return &Source{
		path:     dir + string(os.PathSeparator) + DBFile,
		log:      log,
		interval: 30 * time.Second,
		grace:    30 * time.Second,
	}
}

// UpdatedAt is the modification time of the published database that is currently served
// (zero while there is none).
func (s *Source) UpdatedAt() time.Time {
	if s.last == nil {
		return time.Time{}
	}
	return s.last.ModTime()
}

// Reader returns the current reader, or nil while no database has been published yet.
func (s *Source) Reader() *Reader { return s.cur.Load() }

// Refresh checks the file and switches to a new reader when it was replaced.
func (s *Source) Refresh() error {
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing published yet (or briefly absent): keep what we have
		}
		return err
	}
	if s.last != nil && os.SameFile(s.last, info) && s.last.ModTime().Equal(info.ModTime()) && s.last.Size() == info.Size() {
		return nil
	}
	r, err := OpenReader(s.path)
	if err != nil {
		return err
	}
	old := s.cur.Swap(r)
	s.last = info
	if old != nil {
		// in-flight queries may still use it
		time.AfterFunc(s.grace, func() { _ = old.Close() })
	}
	s.log.Info("database opened", "path", s.path, "size", info.Size())
	return nil
}

// Run polls for a replaced file until ctx ends.
func (s *Source) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(); err != nil {
				s.log.Error("refreshing the database failed; keeping the previous one", "err", err)
			}
		}
	}
}

// Close closes the current reader.
func (s *Source) Close() {
	if r := s.cur.Swap(nil); r != nil {
		_ = r.Close()
	}
}
