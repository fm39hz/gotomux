package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
)

type Storer interface {
	Close() error
	Prune()

	Get(name string) (*model.Session, error)
	Save(s *model.Session) error
	Delete(name string) error
	ListNames() ([]string, error)
	ListMeta() ([]PresetMeta, error)
	Touch(name string) error
	RebindName(old, newName string) error

	AllUsage() (map[string]Usage, error)
	RecordOpen(name string) error
	RecordKill(name string) error
	RecordPair(a, b string) error
	RecordPairsWithLive(name string, live []string)
	PairScores(ctx string, now int64) (map[string]int64, error)

	SaveFreeze(s *model.Session, shapeID, shapeKey, shapeBody string, setSticky bool) (outShapeID string, shapeCreated bool, err error)
	StickShape(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error)
	RememberShapeOnly(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error)
	GetShape(id string) (body string, ok bool)
	GetShapeByKey(key string) (id, body string, ok bool)
	GetShapeMeta(id string) (body string, updated int64, ok bool)
	PutShape(id, key, body string) (outID string, created bool, err error)
	UpsertShapeByID(id, key, body string) error
	ListShapes() ([]string, error)
	StickyID() string
	SetSticky(shapeID string) error

	RecordPlacement(shapeID, pattern string) error
	BestPlacement(shapeID string) (pattern string, ok bool)
	RecordFork(key, body string) error
	ForkHits(key string) int64

	LoadZox() (rows []ZoxRow, updated int64, ok bool)
	SaveZox(rows []ZoxRow) error
}

type Store struct {
	db  *sql.DB
	cfg *config.Config
}

func Open() (*Store, error) { return OpenWithConfig(nil) }

func OpenWithConfig(cfg *config.Config) (*Store, error) {
	var dir string
	if cfg != nil {
		dir = cfg.ResolveDataDir()
	} else {
		var err error
		dir, err = DataDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	_ = migrateLegacyDB(dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, cfg: cfg}
	if err := s.pragma(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Prune is deliberately NOT called here. It issues two DELETEs — a write
	// transaction plus WAL churn — on the cold path of every single CLI
	// invocation, to collect rows that at most trickle in. The daemon runs it
	// periodically instead (see pollLoop).
	return s, nil
}

// pragma: personal-tool hygiene - WAL + reasonable sync + busy wait.
func (s *Store) pragma() error {
	// modernc accepts these; ignore failures on exotic builds
	pragmas := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 1000`,
		`PRAGMA temp_store = MEMORY`,
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func DataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "gotomux"), nil
}

// migrateLegacyDB copies state.db from old product dirs once when gotomux has no sessions.
// Does not keep reading legacy paths afterward.
func migrateLegacyDB(dir string) error {
	dst := filepath.Join(dir, "state.db")
	// already has content?
	if hasSessionRows(dst) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	roots := []string{filepath.Join(home, ".local", "share")}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		roots = append([]string{xdg}, roots...)
	}
	var src string
	for _, root := range roots {
		for _, leg := range []string{"tmux_project", "go-tomux"} {
			p := filepath.Join(root, leg, "state.db")
			if hasSessionRows(p) {
				src = p
				break
			}
		}
		if src != "" {
			break
		}
	}
	if src == "" {
		return nil
	}
	// close any WAL on source by best-effort copy of main file only is wrong if WAL pending;
	// use sqlite backup via file copy of db+wal+shm when present.
	for _, suf := range []string{"", "-wal", "-shm"} {
		s := src + suf
		d := dst + suf
		if _, err := os.Stat(s); err != nil {
			continue
		}
		if err := copyFile(s, d); err != nil {
			return err
		}
	}
	return nil
}

func hasSessionRows(dbPath string) bool {
	st, err := os.Stat(dbPath)
	if err != nil || st.Size() == 0 {
		return false
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM session`).Scan(&n)
	return err == nil && n > 0
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the DB connection is alive.
func (s *Store) Ping() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: nil db")
	}
	return s.db.Ping()
}

// schemaVersion is bumped whenever migrate() gains a statement. It gates the
// whole migration behind one PRAGMA read: the CREATE TABLE IF NOT EXISTS
// statements and pragma_table_info probes are individually cheap but there are
// fourteen of them, on the cold path of every invocation, to do nothing.
//
// The migration itself stays additive-only and idempotent — the fast path is an
// optimisation, not a replacement, so a DB from a future version or one that
// somehow lost user_version still gets the full run.
const schemaVersion = 1

func (s *Store) migrate() error {
	var have int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&have); err == nil && have == schemaVersion {
		return nil
	}
	if err := s.migrateAll(); err != nil {
		return err
	}
	_, _ = s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return nil
}

func (s *Store) migrateAll() error {
	_, err := s.db.Exec(`
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS session (
  name       TEXT PRIMARY KEY,
  cwd        TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS window (
  id      INTEGER PRIMARY KEY,
  session TEXT NOT NULL REFERENCES session(name) ON DELETE CASCADE,
  idx     INTEGER NOT NULL,
  name    TEXT NOT NULL DEFAULT '',
  cwd     TEXT NOT NULL DEFAULT '',
  layout  TEXT,
  UNIQUE(session, idx)
);
CREATE TABLE IF NOT EXISTS pane (
  id        INTEGER PRIMARY KEY,
  window_id INTEGER NOT NULL REFERENCES window(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  cwd       TEXT,
  cmd       TEXT,
  UNIQUE(window_id, idx)
);
`)
	if err != nil {
		return err
	}
	// soft-migrate older DBs missing window.cwd
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('window') WHERE name='cwd'`).Scan(&n)
	if n == 0 {
		_, _ = s.db.Exec(`ALTER TABLE window ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`)
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS usage (
  name      TEXT PRIMARY KEY,
  opens     INTEGER NOT NULL DEFAULT 0,
  kills     INTEGER NOT NULL DEFAULT 0,
  last_open INTEGER NOT NULL DEFAULT 0,
  last_kill INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS pair (
  a        TEXT NOT NULL,
  b        TEXT NOT NULL,
  n        INTEGER NOT NULL DEFAULT 0,
  last     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (a, b)
);`); err != nil {
		return err
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS zox_meta (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  updated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS zox_item (
  ord     INTEGER PRIMARY KEY,
  name    TEXT NOT NULL,
  path    TEXT NOT NULL,
  title   TEXT NOT NULL DEFAULT '',
  desc    TEXT NOT NULL DEFAULT '',
  recency INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS shape (
  id         TEXT PRIMARY KEY,
  key        TEXT NOT NULL UNIQUE,
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sticky (
  id       INTEGER PRIMARY KEY CHECK (id = 1),
  shape_id TEXT NOT NULL DEFAULT 'default'
);`); err != nil {
		return err
	}
	// Old DBs have shape without updated_at (CREATE IF NOT EXISTS does not alter).
	//
	// The ALTER and its backfill go in one transaction: a crash between them left
	// every shape at updated_at = 0, which makes the config mirror's
	// `mtime > dbUpd` comparison always pick the file — so any hand-edited shape
	// file would win over the DB forever, with nothing to indicate why.
	var shapeHasUpdated int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('shape') WHERE name='updated_at'`).Scan(&shapeHasUpdated)
	if shapeHasUpdated == 0 {
		tx, txErr := s.db.Begin()
		if txErr != nil {
			return txErr
		}
		if _, err := tx.Exec(`ALTER TABLE shape ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec(`UPDATE shape SET updated_at = created_at WHERE updated_at = 0`); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS placement (
  shape_id TEXT NOT NULL,
  pattern  TEXT NOT NULL,
  n        INTEGER NOT NULL DEFAULT 0,
  last     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (shape_id, pattern)
);`); err != nil {
		return err
	}
	// fork: window-level essence units learned from freeze/sticky (silent).
	// key = paneCount|split|tools - same key = same reusable unit.
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS fork (
  key      TEXT PRIMARY KEY,
  body     TEXT NOT NULL DEFAULT '',
  n        INTEGER NOT NULL DEFAULT 0,
  last     INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_session_last_used ON session(last_used DESC)`)
	return err
}
