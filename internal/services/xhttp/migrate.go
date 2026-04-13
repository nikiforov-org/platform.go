// internal/services/xhttp/migrate.go
package xhttp

import "database/sql"

// Migrate создаёт схему БД, если она ещё не существует.
// Запускается однократно при старте сервиса — идемпотентна.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS xhttp (
			id         BIGSERIAL    PRIMARY KEY,
			name       TEXT         NOT NULL,
			value      TEXT         NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)`)
	return err
}
