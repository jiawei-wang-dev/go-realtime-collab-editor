package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/config"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "create-user" {
		log.Fatal("usage: go run ./cmd/admin create-user --id USER_ID --username USERNAME")
	}
	flags := flag.NewFlagSet("create-user", flag.ExitOnError)
	id := flags.String("id", "", "existing demo user ID")
	username := flags.String("username", "", "username")
	_ = flags.Parse(os.Args[2:])
	if *id == "" || authentication.ValidateUsername(*username) != nil {
		log.Fatal("valid --id and --username are required")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirmation, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if string(password) != string(confirmation) {
		log.Fatal("passwords do not match")
	}
	if err := authentication.ValidatePassword(string(password)); err != nil {
		log.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	for i := range password {
		password[i] = 0
	}
	for i := range confirmation {
		confirmation[i] = 0
	}
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.StorageDriver != config.StorageMySQL {
		log.Fatal("admin user creation requires STORAGE_DRIVER=mysql")
	}
	store, err := mysqlstorage.Open(context.Background(), cfg.MySQL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := migration.Run(context.Background(), store.DB(), "migrations"); err != nil {
		log.Fatal(err)
	}
	repo := authentication.NewMySQLUsers(store.DB())
	if err := repo.CreateUser(context.Background(), authentication.User{ID: *id, Username: *username, PasswordHash: string(hash), Status: authentication.UserActive}); err != nil {
		log.Fatal(err)
	}
	log.Printf("created user %s without changing existing document ownership or permissions", *id)
}
