package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	client "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/credential"
	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

type options struct {
	address    string
	apiAddress string
	caFile     string
	serverName string
	profile    string
	stateDir   string
}

func parseOptions() options {
	address := flag.String("addr", envOr("COLLAB_QUIC_ADDR", "localhost:8443"), "QUIC server address")
	apiAddress := flag.String("api", envOr("COLLAB_API_URL", "http://localhost:8080"), "REST API base URL")
	caFile := flag.String("ca", os.Getenv("COLLAB_CA_FILE"), "trusted CA certificate file")
	serverName := flag.String("server-name", envOr("COLLAB_SERVER_NAME", "localhost"), "TLS server name")
	profile := flag.String("profile", envOr("COLLAB_CLIENT_PROFILE", "default"), "isolated client profile")
	stateDir := flag.String("state-dir", os.Getenv("COLLAB_CLIENT_STATE_DIR"), "client state root (defaults to OS user config directory)")
	flag.Parse()
	return options{
		address:    *address,
		apiAddress: *apiAddress,
		caFile:     *caFile,
		serverName: *serverName,
		profile:    *profile,
		stateDir:   *stateDir,
	}
}

func run() error {
	configuration := parseOptions()
	if configuration.caFile == "" {
		return errors.New("-ca/COLLAB_CA_FILE is required")
	}
	stateStore, err := clientstorage.Open(context.Background(), configuration.stateDir, configuration.profile)
	if err != nil {
		return fmt.Errorf("open profile %q: %w", configuration.profile, err)
	}
	defer stateStore.Close()
	credentialStore := credential.Keyring{}
	refreshToken, err := loadRefreshToken(stateStore, credentialStore, configuration.profile)
	if err != nil {
		return err
	}
	accessToken := os.Getenv("COLLAB_ACCESS_TOKEN")
	if accessToken == "" && refreshToken == "" {
		return errors.New("COLLAB_ACCESS_TOKEN or a securely stored/COLLAB_REFRESH_TOKEN credential is required")
	}
	tokens := client.NewTokenManager(accessToken, refreshToken, client.HTTPRefreshClient{BaseURL: configuration.apiAddress}, time.Minute)
	tokens.SetRefreshPersistence(client.DurableRefreshPersistence{State: stateStore, Credentials: credentialStore, Profile: configuration.profile})
	manager, err := client.NewManager(client.Config{
		SeedEndpoint: configuration.address,
		DialEndpoint: func(ctx context.Context, endpoint string) (client.Connection, error) {
			serverName := configuration.serverName
			if endpoint != configuration.address {
				host, _, splitErr := net.SplitHostPort(endpoint)
				if splitErr != nil {
					return nil, fmt.Errorf("invalid owner endpoint: %w", splitErr)
				}
				serverName = host
			}
			tlsConfig, tlsErr := quictransport.LoadClientTLSConfig(configuration.caFile, serverName)
			if tlsErr != nil {
				return nil, tlsErr
			}
			return quictransport.Dial(ctx, endpoint, quictransport.DialConfig{TLSConfig: tlsConfig})
		},
		Tokens:     tokens,
		StateStore: stateStore,
		OnMessage: func(message protocol.Message) {
			payload, marshalErr := json.Marshal(message)
			if marshalErr != nil {
				log.Printf("encode server message: %v", marshalErr)
				return
			}
			fmt.Println(string(payload))
		},
		OnStateChange: func(status client.Status) {
			log.Printf("connection state=%s attempts=%d", status.State, status.ReconnectAttempts)
		},
	})
	if err != nil {
		return err
	}
	defer manager.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = manager.Close()
	}()
	return runPrompt(manager, configuration.profile)
}

func loadRefreshToken(stateStore clientstorage.Store, credentialStore credential.Store, profile string) (string, error) {
	stored, err := client.ReconcileRefreshAttempt(context.Background(), stateStore, credentialStore, profile)
	if err != nil && !errors.Is(err, credential.ErrNotFound) {
		return "", fmt.Errorf("load secure refresh credential: %w", err)
	}
	if stored.RefreshToken != "" {
		return stored.RefreshToken, nil
	}
	refreshToken := os.Getenv("COLLAB_REFRESH_TOKEN")
	if refreshToken == "" {
		return "", nil
	}
	if err := credentialStore.SetRefreshToken(profile, credential.Value{RefreshToken: refreshToken}); err != nil {
		return "", fmt.Errorf("operating system credential store is required for durable authentication: %w", err)
	}
	return refreshToken, nil
}

func runPrompt(manager *client.Manager, profile string) error {
	printHelp()
	scanner := bufio.NewScanner(os.Stdin)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if err := execute(manager, profile, line); err != nil {
			log.Printf("command failed: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

func execute(manager *client.Manager, profile, line string) error {
	if strings.HasPrefix(line, "{") {
		return executeJSON(manager, line)
	}
	fields := strings.Fields(line)
	switch fields[0] {
	case "help":
		printHelp()
		return nil
	case "status":
		payload, _ := json.MarshalIndent(manager.Status(), "", "  ")
		fmt.Println(string(payload))
		return nil
	case "profile":
		fmt.Println(profile)
		return nil
	case "pending":
		status := manager.Status()
		payload, _ := json.MarshalIndent(map[string]any{"count": len(status.Pending), "pending": status.Pending}, "", "  ")
		fmt.Println(string(payload))
		return nil
	case "join":
		if len(fields) != 2 {
			return errorsUsage("join DOC_ID")
		}
		return manager.Open(fields[1])
	case "reconnect":
		return manager.Reconnect()
	case "disconnect":
		manager.Disconnect()
		return nil
	case "resume":
		if len(fields) != 2 {
			return errorsUsage("resume CURRENT_VERSION")
		}
		version, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return err
		}
		_, current, available := manager.Document()
		if !available || current != version {
			return fmt.Errorf("local complete document is version %d (available=%t); refusing unsafe resume at %d", current, available, version)
		}
		return manager.Reconnect()
	case "edit":
		return executeEdit(manager, fields)
	default:
		return fmt.Errorf("unknown command %q; use help", fields[0])
	}
}

func executeJSON(manager *client.Manager, line string) error {
	var message protocol.Message
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	switch message.Type {
	case protocol.TypeJoin:
		return manager.Open(message.DocID)
	case protocol.TypeResume:
		status := manager.Status()
		if status.DocumentID != message.DocID || status.ServerVersion != message.LastVersion {
			return errorsUsage("managed RESUME must match the locally applied document/version")
		}
		return manager.Reconnect()
	case protocol.TypeEdit:
		if message.Operation == nil {
			return errorsUsage("edit operation is required")
		}
		requestID := message.RequestID
		if requestID == "" {
			requestID = newRequestID()
		}
		return manager.SubmitEditAt(requestID, message.BaseVersion, *message.Operation)
	default:
		return fmt.Errorf("managed client accepts join, resume, and edit messages")
	}
}

func executeEdit(manager *client.Manager, fields []string) error {
	if len(fields) < 4 {
		return errorsUsage("edit insert POSITION TEXT | edit delete POSITION LENGTH")
	}
	position, err := strconv.Atoi(fields[2])
	if err != nil {
		return err
	}
	operation := collab.Operation{Position: position}
	switch fields[1] {
	case string(collab.OperationInsert):
		operation.Kind = collab.OperationInsert
		operation.Text = strings.Join(fields[3:], " ")
	case string(collab.OperationDelete):
		if len(fields) != 4 {
			return errorsUsage("edit delete POSITION LENGTH")
		}
		length, parseErr := strconv.Atoi(fields[3])
		if parseErr != nil {
			return parseErr
		}
		operation.Kind, operation.Length = collab.OperationDelete, length
	default:
		return errorsUsage("edit insert POSITION TEXT | edit delete POSITION LENGTH")
	}
	return manager.SubmitEdit(newRequestID(), operation)
}

func newRequestID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("cli-%d", time.Now().UnixNano())
	}
	return "cli-" + hex.EncodeToString(buffer)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func errorsUsage(usage string) error { return fmt.Errorf("usage: %s", usage) }

func printHelp() {
	fmt.Fprintln(os.Stderr, "commands: join DOC_ID | edit insert POSITION TEXT | edit delete POSITION LENGTH | status | pending | profile | reconnect | disconnect | resume CURRENT_VERSION | exit")
	fmt.Fprintln(os.Stderr, "one-line join/edit/resume protocol JSON remains accepted")
}
