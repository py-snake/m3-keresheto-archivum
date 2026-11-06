package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ErrLocked means another ingest holds the lock.
var ErrLocked = errors.New("store: another ingest is running")

// Publisher implements the central invariant of the design: **a published database file is
// never modified in place**. An ingest works on a private copy, checks it, and swaps it in with
// an atomic rename. Consequences:
//
//   - the web role can open the file read-only and immutable (no WAL, no locks, no side files);
//   - the host user can copy m3.sqlite at any moment and always gets a consistent database;
//   - a crash at any point leaves the previously published file untouched.
type Publisher struct {
	// Dir is the data directory (m3.sqlite, backups/, images/).
	Dir string
	// Keep is the number of rotated backups of previous databases (0 = none).
	Keep int
	// LockWait is how long Run waits for another ingest or export to finish before it gives up
	// with ErrLocked. 0 = do not wait. Waiting lets two timers that fire at about the same time run
	// one after the other instead of the second one failing.
	LockWait time.Duration
	// MaxNew is a sanity limit for the rows one run may add (0 = default).
	MaxNew int
	// Now is the clock (tests).
	Now func() time.Time

	// stage is called between the steps of Run; tests use it to simulate a crash at a given point.
	stage func(name string)
}

func (p *Publisher) at(name string) {
	if p.stage != nil {
		p.stage(name)
	}
}

// Summary describes a publication.
type Summary struct {
	Before, After int
	Published     bool
}

const defaultMaxNew = 100000

// Run executes work against a private copy of the database and publishes the copy if work
// succeeds and the checks pass. If anything fails the published database is left untouched.
func (p *Publisher) Run(ctx context.Context, work func(ctx context.Context, w *Writer) error) (sum Summary, err error) {
	now := p.Now
	if now == nil {
		now = time.Now
	}
	maxNew := p.MaxNew
	if maxNew <= 0 {
		maxNew = defaultMaxNew
	}

	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return sum, err
	}
	unlock, err := LockWait(ctx, p.Dir, p.LockWait)
	if err != nil {
		return sum, err
	}
	defer unlock()

	live := filepath.Join(p.Dir, DBFile)
	next := live + ".new"
	removeWorkFiles(next)
	defer func() {
		if err != nil || !sum.Published {
			removeWorkFiles(next)
		}
	}()

	if _, statErr := os.Stat(live); statErr == nil {
		if err := copyFile(live, next); err != nil {
			return sum, fmt.Errorf("copying the database: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return sum, statErr
	}

	db, err := openWriter(next)
	if err != nil {
		return sum, err
	}
	closed := false
	closeDB := func() error {
		if closed {
			return nil
		}
		closed = true
		return db.Close()
	}
	defer closeDB() //nolint:errcheck

	if err := initSchema(ctx, db); err != nil {
		return sum, err
	}
	w := newWriter(db, now)

	if sum.Before, err = w.Count(ctx); err != nil {
		return sum, err
	}
	p.at("copied")
	if err := work(ctx, w); err != nil {
		return sum, err
	}
	p.at("worked")
	if err := w.check(ctx); err != nil {
		return sum, err
	}
	p.at("checked")
	if sum.After, err = w.Count(ctx); err != nil {
		return sum, err
	}
	if sum.After < sum.Before {
		return sum, fmt.Errorf("refusing to publish: the row count dropped from %d to %d", sum.Before, sum.After)
	}
	if sum.After-sum.Before > maxNew {
		return sum, fmt.Errorf("refusing to publish: %d new rows in one run exceeds the limit of %d", sum.After-sum.Before, maxNew)
	}
	if err := closeDB(); err != nil {
		return sum, err
	}
	if _, statErr := os.Stat(next + "-journal"); statErr == nil {
		return sum, errors.New("refusing to publish: a hot journal is left behind")
	}

	if err := os.Chmod(next, 0o644); err != nil {
		return sum, err
	}
	if err := syncFile(next); err != nil {
		return sum, err
	}
	p.at("synced")
	if err := p.backup(live, now()); err != nil {
		return sum, fmt.Errorf("rotating backups: %w", err)
	}
	p.at("backed-up")
	if err := os.Rename(next, live); err != nil {
		return sum, err
	}
	p.at("renamed")
	if err := syncDir(p.Dir); err != nil {
		return sum, err
	}
	sum.Published = true
	return sum, nil
}

// backup keeps the about-to-be-replaced database as a hard link in backups/ and rotates old ones.
func (p *Publisher) backup(live string, now time.Time) error {
	if p.Keep <= 0 {
		return nil
	}
	if _, err := os.Stat(live); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	dir := filepath.Join(p.Dir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := filepath.Join(dir, "m3-"+now.UTC().Format("20060102-150405")+".sqlite")
	if _, err := os.Stat(name); err == nil {
		name = strings.TrimSuffix(name, ".sqlite") + fmt.Sprintf("-%d.sqlite", now.UnixNano()%1000000)
	}
	if err := os.Link(live, name); err != nil {
		if err := copyFile(live, name); err != nil {
			return err
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "m3-") && strings.HasSuffix(e.Name(), ".sqlite") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > p.Keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return nil
}

// Lock takes the exclusive lock of the data directory (the same one the ingest holds), so that
// nothing is published while a long read of the published file (the export) is in progress.
// It returns ErrLocked at once when someone else holds it.
func Lock(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".ingest.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// lockPoll is how often LockWait looks again while the lock is held by someone else.
const lockPoll = 500 * time.Millisecond

// LockWait is Lock, but when the lock is held it keeps trying for up to wait before it returns
// ErrLocked (wait <= 0: try once). It returns the context's error when ctx ends first.
func LockWait(ctx context.Context, dir string, wait time.Duration) (unlock func(), err error) {
	deadline := time.Now().Add(wait)
	for {
		unlock, err = Lock(dir)
		if !errors.Is(err, ErrLocked) || wait <= 0 {
			return unlock, err
		}
		if !time.Now().Before(deadline) {
			return nil, ErrLocked
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

func removeWorkFiles(next string) {
	for _, s := range []string{"", "-journal", "-wal", "-shm"} {
		_ = os.Remove(next + s)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
