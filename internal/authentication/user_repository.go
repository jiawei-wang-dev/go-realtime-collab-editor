package authentication

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Status       UserStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at,omitempty"`
}
type UserRepository interface {
	CreateUser(context.Context, User) error
	GetUserByID(context.Context, string) (User, error)
	GetUserByUsername(context.Context, string) (User, error)
	UpdatePassword(context.Context, string, string) error
	SetUserStatus(context.Context, string, UserStatus) error
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return ValidationError("username must be 3-32 characters using letters, numbers, _, - or .")
	}
	return nil
}
func ValidatePassword(password string) error {
	length := len([]byte(password))
	if length < 8 {
		return ValidationError("password must be at least 8 bytes")
	}
	if length > 72 {
		return ValidationError("password must not exceed 72 bytes")
	}
	return nil
}

type MemoryUsers struct {
	mu     sync.RWMutex
	byID   map[string]User
	byName map[string]string
}

func NewMemoryUsers() *MemoryUsers {
	return &MemoryUsers{byID: map[string]User{}, byName: map[string]string{}}
}
func (m *MemoryUsers) CreateUser(ctx context.Context, user User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(user.Username)
	if _, ok := m.byName[key]; ok {
		return ErrUsernameExists
	}
	if _, ok := m.byID[user.ID]; ok {
		return ErrUsernameExists
	}
	now := time.Now().UTC()
	user.CreatedAt = now
	user.UpdatedAt = now
	m.byID[user.ID] = user
	m.byName[key] = user.ID
	return nil
}
func (m *MemoryUsers) GetUserByID(ctx context.Context, id string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return user, nil
}
func (m *MemoryUsers) GetUserByUsername(ctx context.Context, username string) (User, error) {
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byName[strings.ToLower(username)]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return m.byID[id], nil
}
func (m *MemoryUsers) UpdatePassword(ctx context.Context, id, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	user.PasswordHash = hash
	user.UpdatedAt = time.Now().UTC()
	m.byID[id] = user
	return nil
}
func (m *MemoryUsers) SetUserStatus(ctx context.Context, id string, status UserStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[id]
	if !ok {
		return ErrUserNotFound
	}
	user.Status = status
	user.UpdatedAt = time.Now().UTC()
	m.byID[id] = user
	return nil
}
