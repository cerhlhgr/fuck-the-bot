package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var files embed.FS

const advisoryLockKey int64 = 0x6675636b626f74

type migration struct {
	version int64
	name    string
	up      string
	down    string
}

func load() ([]migration, error) {
	entries, err := fs.ReadDir(files, "sql")
	if err != nil {
		return nil, err
	}
	byVersion := map[int64]*migration{}
	for _, entry := range entries {
		name := entry.Name()
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid migration filename %q: %w", name, err)
		}
		m := byVersion[version]
		if m == nil {
			m = &migration{version: version}
			byVersion[version] = m
		}
		data, err := files.ReadFile("sql/" + name)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			m.name = strings.TrimSuffix(parts[1], ".up.sql")
			m.up = string(data)
		case strings.HasSuffix(name, ".down.sql"):
			m.down = string(data)
		default:
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
	}
	result := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration %d needs up and down files", m.version)
		}
		result = append(result, *m)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

func withLock(ctx context.Context, pool *pgxpool.Pool, action func(*pgxpool.Conn, []migration) error) error {
	migrations, err := load()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()
	_, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}
	return action(conn, migrations)
}

func Up(ctx context.Context, pool *pgxpool.Pool) error {
	return withLock(ctx, pool, func(conn *pgxpool.Conn, scripts []migration) error {
		rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return err
		}
		applied := map[int64]bool{}
		for rows.Next() {
			var version int64
			if err := rows.Scan(&version); err != nil {
				rows.Close()
				return err
			}
			applied[version] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, script := range scripts {
			if applied[script.version] {
				continue
			}
			if err := execute(ctx, conn, script.up, script.version, script.name, true); err != nil {
				return fmt.Errorf("apply migration %d: %w", script.version, err)
			}
		}
		return nil
	})
}

func Down(ctx context.Context, pool *pgxpool.Pool) error {
	return withLock(ctx, pool, func(conn *pgxpool.Conn, scripts []migration) error {
		var version int64
		err := conn.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, script := range scripts {
			if script.version == version {
				return execute(ctx, conn, script.down, script.version, script.name, false)
			}
		}
		return fmt.Errorf("no down migration for version %d", version)
	})
}

func execute(ctx context.Context, conn *pgxpool.Conn, sql string, version int64, name string, up bool) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	if up {
		_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, version, name)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
