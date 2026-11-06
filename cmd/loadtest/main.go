package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"
)

type configuration struct {
	nodes      []string
	apiURL     string
	caFile     string
	serverName string
	clients    int
	documents  int
	operations int
	duration   time.Duration
	rampUp     time.Duration
}

type result struct {
	Nodes          int     `json:"nodes"`
	Clients        int     `json:"clients"`
	Documents      int     `json:"documents"`
	Operations     int64   `json:"operations"`
	Errors         int64   `json:"errors"`
	Redirects      int64   `json:"redirects"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	RPS            float64 `json:"rps"`
	P50MS          float64 `json:"p50_ms"`
	P95MS          float64 `json:"p95_ms"`
	P99MS          float64 `json:"p99_ms"`
	HeapAllocBytes uint64  `json:"heap_alloc_bytes"`
	Goroutines     int     `json:"goroutines"`
}

type worker struct {
	connection *quictransport.Connection
	documentID string
	version    atomic.Uint64
	acks       sync.Map
	done       chan struct{}
	err        atomic.Value
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadConfiguration()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.duration)
	defer cancel()
	ownerName := "load-owner-" + shortID()
	ownerToken, err := registerAndLogin(ctx, config.apiURL, ownerName)
	if err != nil {
		return err
	}
	documents := make([]string, config.documents)
	for index := range documents {
		documents[index] = fmt.Sprintf("load-%s-%d", shortID(), index)
		if err := createDocument(ctx, config.apiURL, ownerToken, documents[index]); err != nil {
			return err
		}
	}

	workers := make([]*worker, config.clients)
	var redirects atomic.Int64
	for index := range workers {
		username := fmt.Sprintf("load-user-%s-%d", shortID(), index)
		token, err := registerAndLogin(ctx, config.apiURL, username)
		if err != nil {
			return err
		}
		documentID := documents[index%len(documents)]
		if err := setPermission(ctx, config.apiURL, ownerToken, documentID, username, permission.RoleWrite); err != nil {
			return err
		}
		connection, joined, redirectCount, err := connect(ctx, config, token, documentID)
		if err != nil {
			return err
		}
		redirects.Add(int64(redirectCount))
		current := &worker{connection: connection, documentID: documentID, done: make(chan struct{})}
		current.version.Store(joined.Version)
		workers[index] = current
		go current.receive()
		if config.rampUp > 0 && config.clients > 1 {
			timer := time.NewTimer(config.rampUp / time.Duration(config.clients))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	defer func() {
		for _, current := range workers {
			if current != nil {
				_ = current.connection.Close()
			}
		}
	}()

	started := time.Now()
	latencies := make(chan time.Duration, config.clients*config.operations)
	var failures atomic.Int64
	var completed atomic.Int64
	var wait sync.WaitGroup
	for index, current := range workers {
		wait.Add(1)
		go func(workerIndex int, current *worker) {
			defer wait.Done()
			for operationIndex := 0; operationIndex < config.operations; operationIndex++ {
				requestID := fmt.Sprintf("load-%d-%d-%s", workerIndex, operationIndex, shortID())
				ack := make(chan struct{})
				current.acks.Store(requestID, ack)
				operationStarted := time.Now()
				message := protocol.Message{
					Type: protocol.TypeEdit, RequestID: requestID, DocID: current.documentID,
					BaseVersion: current.version.Load(),
					Operation:   &collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "x"},
				}
				if err := current.connection.Send(message); err != nil {
					current.acks.Delete(requestID)
					failures.Add(1)
					return
				}
				select {
				case <-ctx.Done():
					current.acks.Delete(requestID)
					failures.Add(1)
					return
				case <-current.done:
					current.acks.Delete(requestID)
					failures.Add(1)
					return
				case <-ack:
					latencies <- time.Since(operationStarted)
					completed.Add(1)
				}
			}
		}(index, current)
	}
	wait.Wait()
	close(latencies)
	elapsed := time.Since(started)
	measurements := make([]time.Duration, 0, completed.Load())
	for latency := range latencies {
		measurements = append(measurements, latency)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	report := result{
		Nodes: len(config.nodes), Clients: config.clients, Documents: config.documents,
		Operations: completed.Load(), Errors: failures.Load(), Redirects: redirects.Load(),
		ElapsedSeconds: elapsed.Seconds(), HeapAllocBytes: memory.HeapAlloc, Goroutines: runtime.NumGoroutine(),
	}
	if elapsed > 0 {
		report.RPS = float64(report.Operations) / elapsed.Seconds()
	}
	report.P50MS = percentile(measurements, 0.50)
	report.P95MS = percentile(measurements, 0.95)
	report.P99MS = percentile(measurements, 0.99)
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if report.Errors != 0 || int(report.Operations) != config.clients*config.operations {
		return fmt.Errorf("load run incomplete: operations=%d errors=%d", report.Operations, report.Errors)
	}
	return nil
}

func (worker *worker) receive() {
	defer close(worker.done)
	for {
		message, err := worker.connection.Receive()
		if err != nil {
			worker.err.Store(err)
			return
		}
		switch message.Type {
		case protocol.TypeEditApplied:
			worker.version.Store(message.Version)
			if value, ok := worker.acks.LoadAndDelete(message.RequestID); ok {
				close(value.(chan struct{}))
			}
		case protocol.TypeError:
			worker.err.Store(errors.New(message.Message))
			return
		}
	}
}

func connect(ctx context.Context, config configuration, token, documentID string) (*quictransport.Connection, protocol.Message, int, error) {
	endpoint := config.nodes[0]
	visited := map[string]struct{}{}
	for redirects := 0; redirects <= len(config.nodes)+2; redirects++ {
		if _, duplicate := visited[endpoint]; duplicate {
			return nil, protocol.Message{}, redirects, errors.New("owner redirect loop")
		}
		visited[endpoint] = struct{}{}
		tlsConfig, err := quictransport.LoadClientTLSConfig(config.caFile, config.serverName)
		if err != nil {
			return nil, protocol.Message{}, redirects, err
		}
		connection, err := quictransport.Dial(ctx, endpoint, quictransport.DialConfig{TLSConfig: tlsConfig})
		if err != nil {
			return nil, protocol.Message{}, redirects, err
		}
		if err := connection.Authenticate(token); err != nil {
			_ = connection.Close()
			return nil, protocol.Message{}, redirects, err
		}
		if err := connection.Send(protocol.Message{Type: protocol.TypeJoin, DocID: documentID}); err != nil {
			_ = connection.Close()
			return nil, protocol.Message{}, redirects, err
		}
		response, err := connection.Receive()
		if err != nil {
			_ = connection.Close()
			return nil, protocol.Message{}, redirects, err
		}
		if response.Type == protocol.TypeJoined {
			return connection, response, redirects, nil
		}
		_ = connection.Close()
		if response.Type != protocol.TypeOwnerRedirect || response.Endpoint == "" {
			return nil, protocol.Message{}, redirects, fmt.Errorf("unexpected JOIN response %s", response.Type)
		}
		endpoint = response.Endpoint
	}
	return nil, protocol.Message{}, len(config.nodes) + 2, errors.New("too many owner redirects")
}

func loadConfiguration() (configuration, error) {
	config := configuration{
		nodes: strings.Split(os.Getenv("LOAD_NODES"), ","), apiURL: os.Getenv("LOAD_API_URL"),
		caFile: os.Getenv("LOAD_CA_FILE"), serverName: envOr("LOAD_SERVER_NAME", "localhost"),
	}
	if len(config.nodes) == 1 && config.nodes[0] == "" {
		return configuration{}, errors.New("LOAD_NODES must contain one or more QUIC endpoints")
	}
	if config.apiURL == "" || config.caFile == "" {
		return configuration{}, errors.New("LOAD_API_URL and LOAD_CA_FILE are required")
	}
	var err error
	if config.clients, err = positiveInt("LOAD_CLIENTS", 4); err != nil {
		return configuration{}, err
	}
	if config.documents, err = positiveInt("LOAD_DOCUMENTS", 2); err != nil {
		return configuration{}, err
	}
	if config.operations, err = positiveInt("LOAD_OPERATIONS", 20); err != nil {
		return configuration{}, err
	}
	if config.duration, err = positiveDuration("LOAD_DURATION", 30*time.Second); err != nil {
		return configuration{}, err
	}
	if value := os.Getenv("LOAD_RAMP_UP"); value != "" {
		config.rampUp, err = time.ParseDuration(value)
		if err != nil || config.rampUp < 0 {
			return configuration{}, errors.New("LOAD_RAMP_UP must be a non-negative duration")
		}
	}
	return config, nil
}

func positiveInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func positiveDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func registerAndLogin(ctx context.Context, apiURL, username string) (string, error) {
	password := "LoadPassword123!"
	if err := postJSON(ctx, apiURL+"/api/auth/register", map[string]string{"username": username, "password": password}, nil); err != nil {
		return "", err
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := postJSON(ctx, apiURL+"/api/auth/login", map[string]string{"username": username, "password": password}, &tokens); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func createDocument(ctx context.Context, apiURL, token, documentID string) error {
	return authenticatedJSON(ctx, http.MethodPost, apiURL+"/api/documents", token, map[string]string{"id": documentID, "content": ""}, nil)
}

func setPermission(ctx context.Context, apiURL, token, documentID, username string, role permission.Role) error {
	var user struct {
		ID string `json:"id"`
	}
	// Resolve the target user through a login response's /me endpoint.
	targetToken, err := login(ctx, apiURL, username, "LoadPassword123!")
	if err != nil {
		return err
	}
	if err := authenticatedJSON(ctx, http.MethodGet, apiURL+"/api/auth/me", targetToken, nil, &user); err != nil {
		return err
	}
	return authenticatedJSON(ctx, http.MethodPut, apiURL+"/api/documents/"+documentID+"/permissions/"+user.ID, token, map[string]any{"role": role}, nil)
}

func login(ctx context.Context, apiURL, username, password string) (string, error) {
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := postJSON(ctx, apiURL+"/api/auth/login", map[string]string{"username": username, "password": password}, &tokens); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func postJSON(ctx context.Context, endpoint string, body any, response any) error {
	return authenticatedJSON(ctx, http.MethodPost, endpoint, "", body, response)
}

func authenticatedJSON(ctx context.Context, method, endpoint, token string, body any, response any) error {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	httpResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %s", method, endpoint, httpResponse.Status)
	}
	if response != nil {
		return json.NewDecoder(httpResponse.Body).Decode(response)
	}
	return nil
}

func percentile(values []time.Duration, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	index := int(float64(len(values)-1) * fraction)
	return float64(values[index].Microseconds()) / 1000
}

func shortID() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buffer)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
