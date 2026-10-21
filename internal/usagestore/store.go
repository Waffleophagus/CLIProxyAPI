// Package usagestore persists per-attempt usage records in a local SQLite
// database and answers the aggregate queries behind the management usage API.
//
// Writes never block the request path: records are queued on a bounded channel
// and a single background writer inserts them in batched transactions. When the
// queue is full, records are dropped and counted.
package usagestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

const (
	defaultBufferSize    = 4096
	defaultBatchSize     = 256
	defaultFlushInterval = time.Second
	pruneInterval        = 24 * time.Hour
	dropWarnInterval     = time.Minute
)

// Options configures a Store. Zero values select the defaults.
type Options struct {
	// Path is the SQLite database file; its directory is created when missing.
	Path string
	// RetentionDays prunes rows older than this many days; <= 0 keeps everything.
	RetentionDays int
	// BufferSize bounds the in-memory queue between publishers and the writer.
	BufferSize int
	// BatchSize flushes the pending batch once it holds this many rows.
	BatchSize int
	// FlushInterval flushes pending rows and checks retention periodically.
	FlushInterval time.Duration
	// Now returns the current time; tests inject a fake clock.
	Now func() time.Time
	// Tick replaces the flush ticker; tests drive it manually.
	Tick <-chan time.Time
}

// Stats reports writer counters.
type Stats struct {
	Written int64 `json:"written"`
	Dropped int64 `json:"dropped"`
}

// Store is a SQLite-backed usage store with an asynchronous batched writer.
type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time

	in        chan Row
	flushReq  chan chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once

	batchSize int
	tick      <-chan time.Time
	ticker    *time.Ticker

	retentionDays atomic.Int64
	pruneDue      atomic.Bool
	lastPrune     time.Time // writer goroutine only

	written      atomic.Int64
	dropped      atomic.Int64
	lastDropWarn atomic.Int64
}

// Open opens (creating if needed) the database at opts.Path, applies schema
// migrations, prunes expired rows and starts the background writer.
func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("usagestore: path is required")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = defaultBufferSize
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if dir := filepath.Dir(opts.Path); dir != "" {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			return nil, fmt.Errorf("usagestore: create data directory: %w", errMkdir)
		}
	}
	db, errOpen := sql.Open("sqlite", dsn(opts.Path))
	if errOpen != nil {
		return nil, fmt.Errorf("usagestore: open %s: %w", opts.Path, errOpen)
	}
	if errMigrate := migrate(context.Background(), db); errMigrate != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usagestore: migrate %s: %w", opts.Path, errMigrate)
	}
	s := &Store{
		db:        db,
		path:      opts.Path,
		now:       opts.Now,
		in:        make(chan Row, opts.BufferSize),
		flushReq:  make(chan chan struct{}),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		batchSize: opts.BatchSize,
		tick:      opts.Tick,
	}
	if s.tick == nil {
		s.ticker = time.NewTicker(opts.FlushInterval)
		s.tick = s.ticker.C
	}
	s.retentionDays.Store(int64(opts.RetentionDays))
	s.prune()
	go s.run()
	return s, nil
}

// dsn builds a modernc.org/sqlite DSN with WAL and the pragmas every pooled
// connection needs.
func dsn(path string) string {
	q := url.Values{}
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)"} {
		q.Add("_pragma", pragma)
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// DB exposes the underlying handle for read-only queries in tests.
func (s *Store) DB() *sql.DB { return s.db }

// Stats returns writer counters.
func (s *Store) Stats() Stats {
	return Stats{Written: s.written.Load(), Dropped: s.dropped.Load()}
}

// RetentionDays returns the active retention; <= 0 means keep forever.
func (s *Store) RetentionDays() int { return int(s.retentionDays.Load()) }

// SetRetentionDays updates the retention and prunes on the next writer tick.
func (s *Store) SetRetentionDays(days int) {
	if s.retentionDays.Swap(int64(days)) != int64(days) {
		s.pruneDue.Store(true)
	}
}

// Enqueue queues a row for the writer without ever blocking. Rows are dropped
// (and counted) when the queue is full or the store is closed.
func (s *Store) Enqueue(row Row) {
	if s == nil || s.closed.Load() {
		return
	}
	select {
	case s.in <- row:
	default:
		s.recordDrop()
	}
}

func (s *Store) recordDrop() {
	total := s.dropped.Add(1)
	now := s.now().UnixMilli()
	last := s.lastDropWarn.Load()
	if now-last < dropWarnInterval.Milliseconds() || !s.lastDropWarn.CompareAndSwap(last, now) {
		return
	}
	log.Warnf("usage store queue full; dropping usage records (dropped=%d)", total)
}

// Flush blocks until every row queued before the call is written, or ctx ends.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil || s.closed.Load() {
		return nil
	}
	reply := make(chan struct{})
	select {
	case s.flushReq <- reply:
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-reply:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the writer after flushing queued rows and closes the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errClose error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.stop)
		<-s.done
		if s.ticker != nil {
			s.ticker.Stop()
		}
		// Fold the WAL back into the main file so the database is self-contained.
		if _, errCheckpoint := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); errCheckpoint != nil {
			log.Debugf("usage store: wal checkpoint failed: %v", errCheckpoint)
		}
		errClose = s.db.Close()
	})
	return errClose
}

func (s *Store) run() {
	defer close(s.done)
	batch := make([]Row, 0, s.batchSize)
	write := func() {
		if len(batch) == 0 {
			return
		}
		if errInsert := s.insert(batch); errInsert != nil {
			log.Warnf("usage store: failed to write %d records: %v", len(batch), errInsert)
		} else {
			s.written.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}
	drain := func() {
		for {
			select {
			case row := <-s.in:
				batch = append(batch, row)
				if len(batch) >= s.batchSize {
					write()
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case row := <-s.in:
			batch = append(batch, row)
			if len(batch) >= s.batchSize {
				write()
			}
		case <-s.tick:
			write()
			s.prune()
		case reply := <-s.flushReq:
			drain()
			write()
			close(reply)
		case <-s.stop:
			drain()
			write()
			return
		}
	}
}

// prune deletes expired rows at most once per pruneInterval. It runs on the
// writer goroutine (or before it starts), so it never races inserts.
func (s *Store) prune() {
	days := s.RetentionDays()
	if days <= 0 {
		return
	}
	now := s.now()
	if !s.pruneDue.Swap(false) && !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < pruneInterval {
		return
	}
	s.lastPrune = now
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	res, errDelete := s.db.Exec("DELETE FROM requests WHERE ts < ?", cutoff)
	if errDelete != nil {
		log.Warnf("usage store: retention prune failed: %v", errDelete)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Infof("usage store: pruned %d records older than %d days", n, days)
		if _, errVacuum := s.db.Exec("PRAGMA incremental_vacuum"); errVacuum != nil {
			log.Debugf("usage store: incremental vacuum failed: %v", errVacuum)
		}
	}
}
