package authentication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func mysqlUsers(t *testing.T) *MySQLUsers {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured; skipping real MySQL user repository test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewMySQLUsers(db)
}
func TestMySQLUsersCaseInsensitiveUniqueAndStatus(t *testing.T) {
	repo := mysqlUsers(t)
	suffix := time.Now().UnixNano()
	user := User{ID: fmt.Sprintf("user-%d", suffix), Username: fmt.Sprintf("CaseUser%d", suffix), PasswordHash: "bcrypt-hash", Status: UserActive}
	if err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetUserByUsername(context.Background(), user.Username)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != user.ID {
		t.Fatalf("loaded=%#v", loaded)
	}
	duplicate := user
	duplicate.ID += "-other"
	duplicate.Username = "case" + user.Username[4:]
	if err := repo.CreateUser(context.Background(), duplicate); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("case-insensitive duplicate error=%v", err)
	}
	if err := repo.SetUserStatus(context.Background(), user.ID, UserDisabled); err != nil {
		t.Fatal(err)
	}
	loaded, err = repo.GetUserByID(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != UserDisabled {
		t.Fatalf("status=%s", loaded.Status)
	}
}
