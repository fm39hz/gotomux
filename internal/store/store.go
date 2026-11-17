package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store/dbgen"
)

type Storer interface {
	RecipeRepository
	BaselineRepository
	Close() error
	Prune()
	Checkpoint()

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
	RecordTransition(from, to string) error
	TransitionScores(ctx string, now int64) (map[string]int64, error)

	SaveFreeze(s *model.Session, shapeID, shapeKey, shapeBody string, setSticky bool) (outShapeID string, shapeCreated bool, err error)
	SaveFreezeWithBaseline(s *model.Session, baseline *model.Session, shapeID, shapeKey, shapeBody string, setSticky bool) (outShapeID string, shapeCreated bool, err error)
	StickShape(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error)
	RememberShapeOnly(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error)
	GetShape(id string) (body string, ok bool)
	GetShapeByKey(key string) (id, body string, ok bool)

	SaveBaseline(sess *model.Session) error
	SaveInitialBaseline(sess *model.Session) error
	BindPresetIdentity(sess *model.Session) error
	GetBaseline(name string) (*model.Session, error)
	GetBaselineByLogicalID(id string) (*model.Session, error)
	StartReconcile(journal model.ReconcileJournal) error
	UpdateReconcile(id string, status model.ReconcileJournalStatus, completedStep int, lastError string) error
	GetReconcile(id string) (*model.ReconcileJournal, error)
	GetActiveReconcile(sessionKey string) (*model.ReconcileJournal, error)
	ListActiveReconciles() ([]model.ReconcileJournal, error)
	CommitReconcile(sess *model.Session, journalID string) error
	GetShapeMeta(id string) (ShapeMeta, bool)
	SetShapeMirror(id, path, sig string) error
	PutShape(id, key, body string) (outID string, created bool, err error)
	UpsertShapeByID(id, key, body string) error
	ListShapes() ([]string, error)
	StickyID() string
	SetSticky(shapeID string) error

	RecordPlacement(shapeID, pattern string) error
	BestPlacement(shapeID string) (pattern string, ok bool)
	RecordFork(key, body string) error
	ForkHits(key string) int64

	LoadZox() (rows []ZoxRow, updated int64, sig string, ok bool)
	SaveZox(rows []ZoxRow, sig string) error

	LoadRootNeg(path string) (checked int64, ok bool)
	SaveRootNeg(path string) error
	ForgetRootNeg(path string) error
}

// BaselineRepository is the typed persistence boundary used by reconciliation.
// The legacy Session DTO methods remain for edit/shape paths during migration.
type BaselineRepository = model.BaselineRepository
type RecipeRevisionRepository = model.RecipeRevisionRepository
type RecipeRepository = model.RecipeRepository

type Store struct {
	db  *sql.DB
	q   *dbgen.Queries
	cfg *config.Config
}

var storeContext = context.Background()

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
	unlock, err := acquireMigrationLock(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	if err := migrateLegacyDB(dir); err != nil {
		return nil, fmt.Errorf("copy legacy database: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, q: dbgen.New(db), cfg: cfg}
	if err := s.pragma(); err != nil {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	defer cancel()
	if err := migrateDB(ctx, db); err != nil {
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
// Checkpoint folds the WAL back into the main DB.
//
// Nothing did this: the daemon holds the connection open for its whole life, so
// SQLite never got the "last connection closed" moment that normally triggers a
// checkpoint, and the WAL grew without bound (observed at 1.3 MB against a 124 KB
// database). Every reader has to walk the WAL index, so this is a read-latency
// problem, not just disk use. PASSIVE never blocks a writer.
func (s *Store) Checkpoint() {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
}

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
	n, err := dbgen.New(db).CountSessionRows(context.Background())
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

const schemaVersion = currentSchemaVersion
