package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

const migrationLockWait = 30 * time.Second

var addColumnPattern = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?" + `\s+ADD\s+COLUMN\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?" + `\s+(.+?)\s*$`)

type columnSchema struct {
	columnType    string
	dataType      string
	isNullable    string
	columnDefault sql.NullString
	extra         string
}

func Run(ctx context.Context, db *sql.DB, directory string) (runErr error) {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer connection.Close()

	lockName, err := migrationLockName(ctx, connection)
	if err != nil {
		return err
	}
	if err := acquireMigrationLock(ctx, connection, lockName); err != nil {
		return err
	}
	defer func() {
		if err := releaseMigrationLock(connection, lockName); err != nil && runErr == nil {
			runErr = err
		}
	}()

	return runMigrations(ctx, connection, directory)
}

func runMigrations(ctx context.Context, connection *sql.Conn, directory string) error {
	if _, err := connection.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version VARCHAR(255) PRIMARY KEY, applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var count int
		if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, name).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		for _, statement := range strings.Split(string(payload), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err = connection.ExecContext(ctx, statement); err != nil {
				if recoveryErr := recoverDuplicateColumn(ctx, connection, statement, err); recoveryErr != nil {
					return fmt.Errorf("apply migration %s: %w", name, recoveryErr)
				}
			}
		}
		if _, err = connection.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(?)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

func recoverDuplicateColumn(ctx context.Context, connection *sql.Conn, statement string, executionErr error) error {
	var mysqlError *driver.MySQLError
	if !errors.As(executionErr, &mysqlError) || mysqlError.Number != 1060 {
		return executionErr
	}
	table, column, expected, err := expectedAddedColumn(statement)
	if err != nil {
		return fmt.Errorf("cannot verify duplicate column: %w", err)
	}
	actual, err := loadColumnSchema(ctx, connection, table, column)
	if err != nil {
		return err
	}
	if err := compareColumnSchema(table, column, expected, actual); err != nil {
		return err
	}
	return nil
}

func expectedAddedColumn(statement string) (string, string, columnSchema, error) {
	matches := addColumnPattern.FindStringSubmatch(strings.TrimSpace(statement))
	if len(matches) != 4 {
		return "", "", columnSchema{}, fmt.Errorf("unsupported ADD COLUMN statement %q", statement)
	}
	table, column := matches[1], matches[2]
	definition := strings.ToLower(strings.Join(strings.Fields(matches[3]), " "))
	switch {
	case definition == "bigint unsigned not null default 0":
		return table, column, columnSchema{
			columnType:    "bigint unsigned",
			dataType:      "bigint",
			isNullable:    "NO",
			columnDefault: sql.NullString{String: "0", Valid: true},
		}, nil
	case strings.HasPrefix(definition, "varchar(") && strings.HasSuffix(definition, ") null"):
		length := strings.TrimSuffix(strings.TrimPrefix(definition, "varchar("), ") null")
		parsedLength, parseErr := strconv.Atoi(length)
		if parseErr != nil || parsedLength <= 0 {
			break
		}
		return table, column, columnSchema{
			columnType: "varchar(" + length + ")",
			dataType:   "varchar",
			isNullable: "YES",
		}, nil
	}
	return "", "", columnSchema{}, fmt.Errorf("unsupported column definition %q for %s.%s", matches[3], table, column)
}

func loadColumnSchema(ctx context.Context, connection *sql.Conn, table, column string) (columnSchema, error) {
	var schema columnSchema
	err := connection.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column,
	).Scan(&schema.columnType, &schema.dataType, &schema.isNullable, &schema.columnDefault, &schema.extra)
	if errors.Is(err, sql.ErrNoRows) {
		return columnSchema{}, fmt.Errorf("duplicate column %s.%s was not found in information_schema", table, column)
	}
	if err != nil {
		return columnSchema{}, fmt.Errorf("inspect duplicate column %s.%s: %w", table, column, err)
	}
	return schema, nil
}

func compareColumnSchema(table, column string, expected, actual columnSchema) error {
	var mismatches []string
	compare := func(name, want, got string) {
		if !strings.EqualFold(want, got) {
			mismatches = append(mismatches, fmt.Sprintf("%s=%q want %q", name, got, want))
		}
	}
	compare("COLUMN_TYPE", expected.columnType, actual.columnType)
	compare("DATA_TYPE", expected.dataType, actual.dataType)
	compare("IS_NULLABLE", expected.isNullable, actual.isNullable)
	if expected.columnDefault != actual.columnDefault {
		mismatches = append(mismatches, fmt.Sprintf("COLUMN_DEFAULT=%s want %s", formatNullString(actual.columnDefault), formatNullString(expected.columnDefault)))
	}
	compare("EXTRA", expected.extra, actual.extra)
	if len(mismatches) > 0 {
		return fmt.Errorf("duplicate column %s.%s has incompatible schema: %s", table, column, strings.Join(mismatches, ", "))
	}
	return nil
}

func formatNullString(value sql.NullString) string {
	if !value.Valid {
		return "NULL"
	}
	return fmt.Sprintf("%q", value.String)
}

func migrationLockName(ctx context.Context, connection *sql.Conn) (string, error) {
	var database string
	if err := connection.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&database); err != nil {
		return "", fmt.Errorf("read migration database name: %w", err)
	}
	if database == "" {
		return "", fmt.Errorf("migration database is not selected")
	}
	sum := sha256.Sum256([]byte(database))
	return fmt.Sprintf("collab-migrate:%x", sum[:16]), nil
}

func acquireMigrationLock(ctx context.Context, connection *sql.Conn, name string) error {
	var acquired sql.NullInt64
	if err := connection.QueryRowContext(ctx, `SELECT GET_LOCK(?, ?)`, name, int(migrationLockWait.Seconds())).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("acquire migration lock %q timed out", name)
	}
	return nil
}

func releaseMigrationLock(connection *sql.Conn, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released sql.NullInt64
	if err := connection.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, name).Scan(&released); err != nil {
		return fmt.Errorf("release migration lock: %w", err)
	}
	if !released.Valid || released.Int64 != 1 {
		return fmt.Errorf("migration lock %q was not held", name)
	}
	return nil
}
