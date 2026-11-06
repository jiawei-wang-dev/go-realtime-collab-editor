package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func mysqlTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured; skipping real migration integration test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestExistingDatabaseUpgradeIsIdempotentAndPreservesData(t *testing.T) {
	db := mysqlTestDB(t)
	seedLegacyData(t, db)
	var documents, permissions, operations, snapshots int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM document_permissions`).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_logs`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var afterDocuments, afterPermissions, afterOperations, afterSnapshots, migrations, users, fencingColumns int
	_ = db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&afterDocuments)
	_ = db.QueryRow(`SELECT COUNT(*) FROM document_permissions`).Scan(&afterPermissions)
	_ = db.QueryRow(`SELECT COUNT(*) FROM operation_logs`).Scan(&afterOperations)
	_ = db.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&afterSnapshots)
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations)
	_ = db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='users'`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND ((table_name='documents' AND column_name IN ('fencing_token','owner_node_id')) OR (table_name='operation_logs' AND column_name='fencing_token'))`).Scan(&fencingColumns)
	// Other integration-test packages share the isolated MySQL instance and may
	// add rows concurrently. A migration must never remove existing rows; the
	// fixture-specific assertions below verify that the original data is intact.
	if afterDocuments < documents || afterPermissions < permissions || afterOperations < operations || afterSnapshots < snapshots || migrations < 3 || users != 1 || fencingColumns != 3 {
		t.Fatalf("before docs/perms/ops/snapshots=%d/%d/%d/%d after=%d/%d/%d/%d migrations=%d users=%d fencing_columns=%d", documents, permissions, operations, snapshots, afterDocuments, afterPermissions, afterOperations, afterSnapshots, migrations, users, fencingColumns)
	}
	var content string
	var version, documentFencingToken, operationFencingToken uint64
	var ownerNodeID sql.NullString
	if err := db.QueryRow(`SELECT content, version, owner_node_id, fencing_token FROM documents WHERE id='phase87-preserved-document'`).Scan(&content, &version, &ownerNodeID, &documentFencingToken); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT fencing_token FROM operation_logs WHERE document_id='phase87-preserved-document' AND version=1`).Scan(&operationFencingToken); err != nil {
		t.Fatal(err)
	}
	if content != "preserved-content" || version != 1 || ownerNodeID.Valid || documentFencingToken != 0 || operationFencingToken != 0 {
		t.Fatalf("preserved document = %q version=%d owner=%v document_token=%d operation_token=%d", content, version, ownerNodeID, documentFencingToken, operationFencingToken)
	}
	assertColumnSchema(t, db, "documents", "fencing_token", columnSchema{columnType: "bigint unsigned", dataType: "bigint", isNullable: "NO", columnDefault: sql.NullString{String: "0", Valid: true}})
	assertColumnSchema(t, db, "documents", "owner_node_id", columnSchema{columnType: "varchar(128)", dataType: "varchar", isNullable: "YES"})
	assertColumnSchema(t, db, "operation_logs", "fencing_token", columnSchema{columnType: "bigint unsigned", dataType: "bigint", isNullable: "NO", columnDefault: sql.NullString{String: "0", Valid: true}})
}

func TestFreshMigrationCreatesExpectedSchema(t *testing.T) {
	db := mysqlTestDB(t)
	var migrationTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='schema_migrations'`).Scan(&migrationTable); err != nil {
		t.Fatal(err)
	}
	if migrationTable != 0 {
		t.Skip("fresh migration test requires an empty isolated database")
	}
	if err := Run(context.Background(), db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"documents", "document_permissions", "operation_logs", "snapshots", "users", "schema_migrations"} {
		assertTableExists(t, db, table, true)
	}
	assertMigrationCount(t, db, "001_initial.sql", 1)
	assertMigrationCount(t, db, "002_users.sql", 1)
	assertMigrationCount(t, db, "003_fencing_tokens.sql", 1)
	assertColumnSchema(t, db, "documents", "fencing_token", columnSchema{columnType: "bigint unsigned", dataType: "bigint", isNullable: "NO", columnDefault: sql.NullString{String: "0", Valid: true}})
	assertColumnSchema(t, db, "documents", "owner_node_id", columnSchema{columnType: "varchar(128)", dataType: "varchar", isNullable: "YES"})
	assertColumnSchema(t, db, "operation_logs", "fencing_token", columnSchema{columnType: "bigint unsigned", dataType: "bigint", isNullable: "NO", columnDefault: sql.NullString{String: "0", Valid: true}})
}

func TestMigrationFailureCanResumeWithoutFalseCompletion(t *testing.T) {
	db := mysqlTestDB(t)
	const (
		version     = "900_phase8_recovery.sql"
		firstTable  = "phase8_migration_recovery_first"
		secondTable = "phase8_migration_recovery_second"
	)
	cleanupMigrationFixture(t, db, version, firstTable, secondTable)
	directory := t.TempDir()
	path := filepath.Join(directory, version)
	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); INVALID PHASE8 STATEMENT; CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))

	if err := Run(context.Background(), db, directory); err == nil {
		t.Fatal("broken migration succeeded")
	}
	assertTableExists(t, db, firstTable, true)
	assertTableExists(t, db, secondTable, false)
	assertMigrationCount(t, db, version, 0)

	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertTableExists(t, db, firstTable, true)
	assertTableExists(t, db, secondTable, true)
	assertMigrationCount(t, db, version, 1)
}

func TestConcurrentMigrationRunnersSerialize(t *testing.T) {
	db := mysqlTestDB(t)
	const (
		version = "901_phase8_concurrent.sql"
		table   = "phase8_migration_concurrent"
	)
	cleanupMigrationFixture(t, db, version, table)
	directory := t.TempDir()
	writeMigration(t, filepath.Join(directory, version), fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); DO SLEEP(0.2);`, table))

	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errors <- Run(context.Background(), db, directory)
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertMigrationCount(t, db, version, 1)
}

func TestAddColumnMigrationResumesAfterPartialApplication(t *testing.T) {
	db := mysqlTestDB(t)
	const (
		version = "903_phase8_add_column.sql"
		table   = "phase8_migration_add_column"
	)
	cleanupMigrationFixture(t, db, version, table)
	if _, err := db.Exec(`CREATE TABLE ` + table + `(id BIGINT PRIMARY KEY, fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	writeMigration(t, filepath.Join(directory, version), fmt.Sprintf(`ALTER TABLE %s ADD COLUMN fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0; ALTER TABLE %s ADD COLUMN owner_node_id VARCHAR(128) NULL;`, table, table))
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name IN ('fencing_token','owner_node_id')`, table).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 2 {
		t.Fatalf("fencing columns = %d, want 2", columns)
	}
	assertMigrationCount(t, db, version, 1)
}

func TestAddColumnMigrationRejectsIncompatibleExistingSchema(t *testing.T) {
	db := mysqlTestDB(t)
	tests := []struct {
		name       string
		definition string
		wantField  string
	}{
		{name: "data type", definition: "INT UNSIGNED NOT NULL DEFAULT 0", wantField: "COLUMN_TYPE"},
		{name: "missing unsigned", definition: "BIGINT NOT NULL DEFAULT 0", wantField: "COLUMN_TYPE"},
		{name: "nullable", definition: "BIGINT UNSIGNED NULL DEFAULT 0", wantField: "IS_NULLABLE"},
		{name: "default", definition: "BIGINT UNSIGNED NOT NULL DEFAULT 1", wantField: "COLUMN_DEFAULT"},
		{name: "extra", definition: "BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY", wantField: "EXTRA"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := fmt.Sprintf("91%d_phase8_incompatible.sql", index)
			table := fmt.Sprintf("phase8_migration_incompatible_%d", index)
			cleanupMigrationFixture(t, db, version, table)
			if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s(fencing_token %s)`, table, test.definition)); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			writeMigration(t, filepath.Join(directory, version), fmt.Sprintf(`ALTER TABLE %s ADD COLUMN fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0; ALTER TABLE %s ADD COLUMN owner_node_id VARCHAR(128) NULL;`, table, table))
			err := Run(context.Background(), db, directory)
			if err == nil || !strings.Contains(err.Error(), test.wantField) {
				t.Fatalf("error = %v, want incompatible %s", err, test.wantField)
			}
			assertMigrationCount(t, db, version, 0)
			assertColumnAbsent(t, db, table, "owner_node_id")
		})
	}
}

func TestCancelledMigrationCanResume(t *testing.T) {
	db := mysqlTestDB(t)
	const (
		version     = "902_phase8_cancelled.sql"
		firstTable  = "phase8_migration_cancelled_first"
		secondTable = "phase8_migration_cancelled_second"
	)
	cleanupMigrationFixture(t, db, version, firstTable, secondTable)
	directory := t.TempDir()
	path := filepath.Join(directory, version)
	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); DO SLEEP(5); CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Run(ctx, db, directory); err == nil {
		t.Fatal("cancelled migration succeeded")
	}
	assertMigrationCount(t, db, version, 0)

	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatal(err)
	}
	assertTableExists(t, db, firstTable, true)
	assertTableExists(t, db, secondTable, true)
	assertMigrationCount(t, db, version, 1)
}

func TestInterruptedConnectionCanResume(t *testing.T) {
	if os.Getenv("MYSQL_TEST_ALLOW_CONNECTION_KILL") != "1" {
		t.Skip("MYSQL_TEST_ALLOW_CONNECTION_KILL=1 is required for connection interruption test")
	}
	db := mysqlTestDB(t)
	const (
		version     = "904_phase8_interrupted.sql"
		firstTable  = "phase8_migration_interrupted_first"
		secondTable = "phase8_migration_interrupted_second"
	)
	cleanupMigrationFixture(t, db, version, firstTable, secondTable)
	directory := t.TempDir()
	path := filepath.Join(directory, version)
	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); DO SLEEP(10); CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), db, directory) }()
	connectionID := waitForMigrationSleep(t, db)
	if _, err := db.Exec(fmt.Sprintf("KILL CONNECTION %d", connectionID)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("migration succeeded after its connection was killed")
	}
	assertTableExists(t, db, firstTable, true)
	assertTableExists(t, db, secondTable, false)
	assertMigrationCount(t, db, version, 0)

	writeMigration(t, path, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY); CREATE TABLE IF NOT EXISTS %s(id BIGINT PRIMARY KEY);`, firstTable, secondTable))
	if err := Run(context.Background(), db, directory); err != nil {
		t.Fatal(err)
	}
	assertTableExists(t, db, secondTable, true)
	assertMigrationCount(t, db, version, 1)
}

func writeMigration(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cleanupMigrationFixture(t *testing.T, db *sql.DB, version string, tables ...string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(version VARCHAR(255) PRIMARY KEY, applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		for _, table := range tables {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
				t.Errorf("drop test table %s: %v", table, err)
			}
		}
		if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=?`, version); err != nil {
			t.Errorf("delete test migration %s: %v", version, err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
}

func assertTableExists(t *testing.T, db *sql.DB, table string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if got := count == 1; got != want {
		t.Fatalf("table %s exists=%t, want %t", table, got, want)
	}
}

func assertMigrationCount(t *testing.T, db *sql.DB, version string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("migration %s count=%d, want %d", version, count, want)
	}
}

func assertColumnSchema(t *testing.T, db *sql.DB, table, column string, expected columnSchema) {
	t.Helper()
	var actual columnSchema
	if err := db.QueryRow(`SELECT COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, table, column).
		Scan(&actual.columnType, &actual.dataType, &actual.isNullable, &actual.columnDefault, &actual.extra); err != nil {
		t.Fatal(err)
	}
	if err := compareColumnSchema(table, column, expected, actual); err != nil {
		t.Fatal(err)
	}
}

func assertColumnAbsent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, table, column).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("column %s.%s exists", table, column)
	}
}

func seedLegacyData(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM documents WHERE id='phase87-preserved-document'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM documents WHERE id='phase87-preserved-document'`) })
	if _, err := db.Exec(`INSERT INTO documents(id,owner_id,content,version) VALUES('phase87-preserved-document','phase87-owner','preserved-content',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO document_permissions(document_id,user_id,role) VALUES('phase87-preserved-document','phase87-owner','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operation_logs(document_id,version,request_id,user_id,base_version,operation_kind,position,text,delete_length) VALUES('phase87-preserved-document',1,'phase87-request','phase87-owner',0,'insert',0,'preserved-content',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO snapshots(document_id,version,content) VALUES('phase87-preserved-document',0,'')`); err != nil {
		t.Fatal(err)
	}
}

func waitForMigrationSleep(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var connectionID int64
		err := db.QueryRow(`SELECT ID FROM information_schema.processlist WHERE DB=DATABASE() AND INFO='DO SLEEP(10)' LIMIT 1`).Scan(&connectionID)
		if err == nil {
			return connectionID
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("migration sleep query was not observed")
	return 0
}
