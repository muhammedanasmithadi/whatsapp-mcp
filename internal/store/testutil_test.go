package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed testdata/seed.sql
var testFixtures embed.FS

var uniqueDBCounter atomic.Uint64

func openTestStore(t *testing.T) *Store {
	t.Helper()

	id := uniqueDBCounter.Add(1)
	dsn := fmt.Sprintf("file:store_test_%d?mode=memory&cache=shared&_foreign_keys=on", id)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)

	seed, err := testFixtures.ReadFile("testdata/seed.sql")
	if err != nil {
		db.Close()
		t.Fatalf("read seed.sql: %v", err)
	}
	if _, err := db.Exec(string(seed)); err != nil {
		db.Close()
		t.Fatalf("apply seed: %v", err)
	}

	waDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("open whatsmeow db handle: %v", err)
	}
	waDB.SetMaxOpenConns(1)

	s := &Store{
		db:          db,
		whatsmeowDB: waDB,
		dir:         t.TempDir(),
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}
