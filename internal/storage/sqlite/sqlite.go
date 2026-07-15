package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = domain.ErrNotFound
	ErrSessionExpired = domain.ErrSessionExpired
)

type Store struct {
	db    *sql.DB
	nowMS func() int64
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("open sqlite: database path is empty")
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &Store{
		db: db,
		nowMS: func() int64 {
			return time.Now().UTC().UnixMilli()
		},
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func sqliteDSN(path string) string {
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "foreign_keys(ON)")
	parameters.Add("_pragma", "journal_mode(WAL)")

	if strings.HasPrefix(path, "file:") {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		return path + separator + parameters.Encode()
	}
	if path == ":memory:" {
		return "file::memory:?" + parameters.Encode()
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: parameters.Encode()}
	return uri.String()
}

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}
