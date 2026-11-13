package server

import (
	"database/sql"
	"fmt"
	"testing"
)

// A database from a pre-release 0.5.0 build: migrations 1–5, then today's
// 12–16 under the numbers 6–10. migrate must add 6–11 and keep the rest.
func TestMigrateRenumberedDatabase(t *testing.T) {
	db, err := openDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// openDB migrated everything: build the old layout from scratch instead
	db2, err := sql.Open("sqlite", "file:"+t.TempDir()+"/old.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	db2.SetMaxOpenConns(1)
	for i := 0; i < 5; i++ {
		mustExec(t, db2, migrations[i])
	}
	for i := 11; i < 16; i++ {
		mustExec(t, db2, migrations[i])
	}
	mustExec(t, db2, `INSERT INTO licenses (name, kind, count_admins, created_at) VALUES ('Comm', 'commercial', 0, 0)`)
	mustExec(t, db2, `PRAGMA user_version = 10`)

	if err := migrate(db2); err != nil {
		t.Fatal(err)
	}
	var v, admins, dl int
	db2.QueryRow(`PRAGMA user_version`).Scan(&v)
	db2.QueryRow(`SELECT count_admins, downloadable FROM licenses WHERE name = 'Comm'`).Scan(&admins, &dl)
	if v != len(migrations) || !hasColumn(db2, "collections", "mode") || !hasColumn(db2, "licenses", "lease_days") || admins != 0 || dl != 0 {
		t.Fatalf("version %d, mode %v, lease %v, count_admins %d, downloadable %d", v, hasColumn(db2, "collections", "mode"),
			hasColumn(db2, "licenses", "lease_days"), admins, dl)
	}
	// a normal database is left to the regular path
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(fmt.Errorf("%s: %w", q[:min(len(q), 60)], err))
	}
}
