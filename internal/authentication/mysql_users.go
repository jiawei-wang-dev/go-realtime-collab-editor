package authentication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	driver "github.com/go-sql-driver/mysql"
)

type MySQLUsers struct{ db *sql.DB }

func NewMySQLUsers(db *sql.DB) *MySQLUsers { return &MySQLUsers{db: db} }
func (r *MySQLUsers) CreateUser(ctx context.Context, user User) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,status) VALUES(?,?,?,?)`, user.ID, user.Username, user.PasswordHash, user.Status)
	if duplicate(err) {
		return ErrUsernameExists
	}
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}
func (r *MySQLUsers) GetUserByID(ctx context.Context, id string) (User, error) {
	return r.get(ctx, `SELECT id,username,password_hash,status,created_at,updated_at FROM users WHERE id=?`, id)
}
func (r *MySQLUsers) GetUserByUsername(ctx context.Context, name string) (User, error) {
	return r.get(ctx, `SELECT id,username,password_hash,status,created_at,updated_at FROM users WHERE username=?`, name)
}
func (r *MySQLUsers) get(ctx context.Context, query, value string) (User, error) {
	var user User
	err := r.db.QueryRowContext(ctx, query, value).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}
func (r *MySQLUsers) UpdatePassword(ctx context.Context, id, hash string) error {
	return r.update(ctx, `UPDATE users SET password_hash=? WHERE id=?`, hash, id)
}
func (r *MySQLUsers) SetUserStatus(ctx context.Context, id string, status UserStatus) error {
	return r.update(ctx, `UPDATE users SET status=? WHERE id=?`, status, id)
}
func (r *MySQLUsers) update(ctx context.Context, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return nil
}
func duplicate(err error) bool {
	var mysqlErr *driver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

var _ UserRepository = (*MySQLUsers)(nil)
