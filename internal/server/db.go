package server

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	display_name  TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	password_hash TEXT,
	oidc_subject  TEXT UNIQUE,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	disabled      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);
CREATE TABLE groups (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE user_groups (
	user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, group_id)
);
CREATE TABLE licenses (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	vendor     TEXT NOT NULL DEFAULT '',
	seats      INTEGER NOT NULL DEFAULT 0, -- 0 = unlimited
	notes      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE fonts (
	id              INTEGER PRIMARY KEY,
	sha256          TEXT NOT NULL,
	face_index      INTEGER NOT NULL DEFAULT 0,
	filename        TEXT NOT NULL,
	format          TEXT NOT NULL,
	size            INTEGER NOT NULL,
	family          TEXT NOT NULL,
	style           TEXT NOT NULL,
	full_name       TEXT NOT NULL,
	postscript_name TEXT NOT NULL DEFAULT '',
	version         TEXT NOT NULL DEFAULT '',
	designer        TEXT NOT NULL DEFAULT '',
	manufacturer    TEXT NOT NULL DEFAULT '',
	copyright       TEXT NOT NULL DEFAULT '',
	license_text    TEXT NOT NULL DEFAULT '',
	license_url     TEXT NOT NULL DEFAULT '',
	weight          INTEGER NOT NULL DEFAULT 400,
	italic          INTEGER NOT NULL DEFAULT 0,
	fs_type         INTEGER NOT NULL DEFAULT 0,
	license_id      INTEGER REFERENCES licenses(id) ON DELETE SET NULL,
	uploaded_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at      INTEGER NOT NULL,
	UNIQUE (sha256, face_index)
);
CREATE INDEX fonts_family ON fonts(family COLLATE NOCASE);
CREATE TABLE collections (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT '',
	everyone    INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);
CREATE TABLE collection_fonts (
	collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
	font_id       INTEGER NOT NULL REFERENCES fonts(id) ON DELETE CASCADE,
	PRIMARY KEY (collection_id, font_id)
);
CREATE TABLE collection_groups (
	collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
	group_id      INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
	PRIMARY KEY (collection_id, group_id)
);
CREATE TABLE activations (
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	font_id    INTEGER NOT NULL REFERENCES fonts(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, font_id)
);
CREATE TABLE devices (
	id            INTEGER PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	platform      TEXT NOT NULL DEFAULT '',
	hostname      TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	token_hash    TEXT NOT NULL UNIQUE,
	last_seen     INTEGER,
	last_ip       TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL
);
CREATE TABLE device_fonts (
	device_id    INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	sha256       TEXT NOT NULL,
	installed_at INTEGER NOT NULL,
	PRIMARY KEY (device_id, sha256)
);
CREATE TABLE enroll_codes (
	code_hash  TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE audit (
	id      INTEGER PRIMARY KEY,
	ts      INTEGER NOT NULL,
	user_id INTEGER,
	actor   TEXT NOT NULL DEFAULT '',
	action  TEXT NOT NULL,
	detail  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_ts ON audit(ts);
`,
	// 2: remember where a group membership came from, so SSO group sync
	// only manages memberships it created
	`
ALTER TABLE user_groups ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
CREATE INDEX fonts_psname ON fonts(postscript_name) WHERE postscript_name != '';
`,
	// 3: every font format – kind (sfnt, type1, winfnt, pcf, bdf), the
	// platforms that can install it, the original format of converted
	// uploads, and companion files (Type 1 metrics)
	`
ALTER TABLE fonts ADD COLUMN kind TEXT NOT NULL DEFAULT 'sfnt';
ALTER TABLE fonts ADD COLUMN platforms TEXT NOT NULL DEFAULT '';
ALTER TABLE fonts ADD COLUMN source_format TEXT NOT NULL DEFAULT '';
ALTER TABLE fonts ADD COLUMN extras TEXT NOT NULL DEFAULT '[]';
`,
	// 4: license management – terms, rights, seat unit, enforcement, term
	// dates and the license documents themselves (EULA, invoice…)
	`
ALTER TABLE licenses ADD COLUMN kind TEXT NOT NULL DEFAULT 'commercial';
ALTER TABLE licenses ADD COLUMN spdx TEXT NOT NULL DEFAULT '';
ALTER TABLE licenses ADD COLUMN rights TEXT NOT NULL DEFAULT '';
ALTER TABLE licenses ADD COLUMN seat_unit TEXT NOT NULL DEFAULT 'users';
ALTER TABLE licenses ADD COLUMN enforce INTEGER NOT NULL DEFAULT 0;
ALTER TABLE licenses ADD COLUMN reference TEXT NOT NULL DEFAULT '';
ALTER TABLE licenses ADD COLUMN url TEXT NOT NULL DEFAULT '';
ALTER TABLE licenses ADD COLUMN purchased_on TEXT NOT NULL DEFAULT '';
ALTER TABLE licenses ADD COLUMN expires_on TEXT NOT NULL DEFAULT '';
CREATE TABLE license_documents (
	id          INTEGER PRIMARY KEY,
	license_id  INTEGER NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
	sha256      TEXT NOT NULL,
	filename    TEXT NOT NULL,
	size        INTEGER NOT NULL,
	shared      INTEGER NOT NULL DEFAULT 1,
	uploaded_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at  INTEGER NOT NULL
);
CREATE INDEX license_documents_license ON license_documents(license_id);
CREATE INDEX fonts_license ON fonts(license_id);
`,
	// 5: whether a license allows downloading the font files directly
	`ALTER TABLE licenses ADD COLUMN downloadable INTEGER NOT NULL DEFAULT 1;`,
	// 6: collections the organisation installs automatically. required_fonts
	// lists what each user gets that way; user_fonts is everything a user
	// has – switched on themselves or installed for them – and is what seat
	// counting, reports and the agents' manifests use.
	`
ALTER TABLE collections ADD COLUMN mode TEXT NOT NULL DEFAULT 'available';
CREATE VIEW required_fonts AS
	SELECT DISTINCT u.id AS user_id, cf.font_id AS font_id, c.id AS collection_id
	FROM collections c
	JOIN collection_fonts cf ON cf.collection_id = c.id
	JOIN users u ON u.disabled = 0 AND (c.everyone = 1 OR EXISTS (
		SELECT 1 FROM collection_groups cg JOIN user_groups ug ON ug.group_id = cg.group_id
		WHERE cg.collection_id = c.id AND ug.user_id = u.id))
	WHERE c.mode = 'installed';
CREATE VIEW user_fonts AS
	SELECT user_id, font_id FROM activations
	UNION
	SELECT user_id, font_id FROM required_fonts;
`,
	// 7: requests for fonts that aren't in the library yet
	`
CREATE TABLE font_requests (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	font        TEXT NOT NULL,
	purpose     TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'open',
	answer      TEXT NOT NULL DEFAULT '',
	seen        INTEGER NOT NULL DEFAULT 1,
	created_at  INTEGER NOT NULL,
	resolved_at INTEGER,
	resolved_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX font_requests_status ON font_requests(status);
`,
	// 8: direct downloads of commercial fonts are opt-in: a downloaded copy
	// is outside typehaus' control (not counted, not removed on revocation)
	`UPDATE licenses SET downloadable = 0 WHERE kind = 'commercial';`,
	// 9: daily usage per license (the day's maximum) – audits ask for peaks
	`
CREATE TABLE license_usage (
	day        TEXT NOT NULL,
	license_id INTEGER NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
	users      INTEGER NOT NULL DEFAULT 0,
	devices    INTEGER NOT NULL DEFAULT 0,
	machines   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (day, license_id)
);
`,
}

func openDB(dataDir string) (*sql.DB, error) {
	dsn := "file:" + filepath.Join(dataDir, "typehaus.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialise writers, avoids SQLITE_BUSY
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
