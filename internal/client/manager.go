package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"
)

var (
	ErrClosed             = errors.New("client is closed")
	ErrReconnectStopped   = errors.New("automatic reconnect is stopped")
	ErrRecoveryValidation = errors.New("recovery validation failed")
	ErrRedirectLoop       = errors.New("owner redirect loop detected")
	ErrInvalidRedirect    = errors.New("invalid owner redirect")
)

type Connection interface {
	Authenticate(string) error
	Send(protocol.Message) error
	Receive() (protocol.Message, error)
	Close() error
}

type DialFunc func(context.Context) (Connection, error)
type EndpointDialFunc func(context.Context, string) (Connection, error)

type Config struct {
	Dial           DialFunc
	DialEndpoint   EndpointDialFunc
	SeedEndpoint   string
	MaxRedirects   int
	Tokens         *TokenManager
	Backoff        *Backoff
	ConnectTimeout time.Duration
	RefreshTimeout time.Duration
	OnMessage      func(protocol.Message)
	OnStateChange  func(Status)
	StateStore     clientstorage.Store
}

type pendingOperation struct {
	requestID  string
	docID      string
	operation  collab.Operation
	state      PendingState
	retryCount int
	err        string
}

type Manager struct {
	config Config

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}

	mu                   sync.Mutex
	state                State
	docID                string
	content              string
	version              uint64
	hasDocument          bool
	authenticated        bool
	reconnectAttempts    int
	lastDisconnectReason string
	pending              []*pendingOperation
	connection           Connection
	automaticReconnect   bool
	forceReconnect       bool
	authRefreshAttempted bool
	updatedAt            time.Time
	currentEndpoint      string
	closeOnce            sync.Once
}

func NewManager(config Config) (*Manager, error) {
	if (config.Dial == nil && config.DialEndpoint == nil) || config.Tokens == nil {
		return nil, errors.New("dialer and token manager are required")
	}
	if config.Backoff == nil {
		config.Backoff = NewBackoff(500*time.Millisecond, 30*time.Second, 0.2)
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	if config.RefreshTimeout <= 0 {
		config.RefreshTimeout = 10 * time.Second
	}
	if config.DialEndpoint != nil && config.SeedEndpoint == "" {
		return nil, errors.New("seed endpoint is required for endpoint-aware dialing")
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = 5
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		config: config, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
		state: StateDisconnected, automaticReconnect: true, updatedAt: time.Now().UTC(), currentEndpoint: config.SeedEndpoint,
	}
	if config.StateStore != nil {
		recovery, err := config.StateStore.Recovery(context.Background())
		if err != nil {
			cancel()
			return nil, fmt.Errorf("recover client state: %w", err)
		}
		manager.docID = recovery.ActiveDocument
		if recovery.Document != nil {
			manager.content = recovery.Document.Content
			manager.version = recovery.Document.ServerVersion
			manager.hasDocument = true
		}
		for _, persisted := range recovery.Pending {
			state := PendingQueued
			if persisted.Status == string(PendingFailed) {
				state = PendingFailed
			}
			manager.pending = append(manager.pending, &pendingOperation{
				requestID: persisted.RequestID, docID: persisted.DocID, operation: persisted.Operation,
				state: state, retryCount: persisted.RetryCount, err: persisted.Error,
			})
		}
	}
	go manager.run()
	return manager, nil
}

func (m *Manager) Open(docID string) error {
	if docID == "" {
		return errors.New("document id is required")
	}
	m.mu.Lock()
	if m.state == StateClosed {
		m.mu.Unlock()
		return ErrClosed
	}
	if m.docID != "" && m.docID != docID && m.hasDocument {
		m.mu.Unlock()
		return errors.New("one client manager supports one document")
	}
	m.docID = docID
	if m.config.StateStore != nil {
		if err := m.config.StateStore.SetActiveDocument(context.Background(), docID); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("persist active document: %w", err)
		}
	}
	m.automaticReconnect = true
	m.forceReconnect = m.connection != nil
	m.mu.Unlock()
	m.signal()
	return nil
}

func (m *Manager) SubmitEdit(requestID string, operation collab.Operation) error {
	m.mu.Lock()
	baseVersion := m.version
	m.mu.Unlock()
	return m.SubmitEditAt(requestID, baseVersion, operation)
}

// SubmitEditAt preserves the caller-provided protocol base version for the
// JSON-compatible CLI and OT demonstrations that intentionally use stale versions.
func (m *Manager) SubmitEditAt(requestID string, baseVersion uint64, operation collab.Operation) error {
	if requestID == "" {
		return errors.New("request_id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StateClosed {
		return ErrClosed
	}
	if m.docID == "" {
		return errors.New("join a document before editing")
	}
	for _, pending := range m.pending {
		if pending.requestID == requestID {
			return errors.New("request_id is already pending")
		}
	}
	operation.BaseVersion = baseVersion
	if m.config.StateStore != nil {
		if err := m.config.StateStore.AddPending(context.Background(), clientstorage.PendingEdit{
			DocID: m.docID, RequestID: requestID, BaseVersion: baseVersion, Operation: operation, Status: string(PendingQueued),
		}); err != nil {
			return fmt.Errorf("persist pending edit before send: %w", err)
		}
	}
	m.pending = append(m.pending, &pendingOperation{requestID: requestID, docID: m.docID, operation: operation, state: PendingQueued})
	m.updatedAt = time.Now().UTC()
	m.signal()
	return nil
}

func (m *Manager) Reconnect() error {
	m.mu.Lock()
	if m.state == StateClosed {
		m.mu.Unlock()
		return ErrClosed
	}
	if m.docID == "" {
		m.mu.Unlock()
		return errors.New("no document selected")
	}
	m.automaticReconnect = true
	m.forceReconnect = true
	m.mu.Unlock()
	m.signal()
	return nil
}

func (m *Manager) Disconnect() {
	m.mu.Lock()
	m.automaticReconnect = false
	m.forceReconnect = true
	m.mu.Unlock()
	m.signal()
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() { m.cancel() })
	<-m.done
	return nil
}

func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) Document() (string, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.content, m.version, m.hasDocument
}

func (m *Manager) statusLocked() Status {
	return Status{
		State: m.state, DocumentID: m.docID, ServerVersion: m.version,
		Authenticated: m.authenticated, Reconnecting: m.state == StateReconnecting,
		ReconnectAttempts: m.reconnectAttempts, LastDisconnectReason: m.lastDisconnectReason,
		Pending: m.pendingStatusesLocked(), UpdatedAt: m.updatedAt,
		OwnerEndpoint: m.currentEndpoint,
	}
}

func (m *Manager) pendingStatusesLocked() []PendingStatus {
	pending := make([]PendingStatus, 0, len(m.pending))
	for _, operation := range m.pending {
		pending = append(pending, PendingStatus{RequestID: operation.requestID, DocumentID: operation.docID, BaseVersion: operation.operation.BaseVersion, Operation: string(operation.operation.Kind), State: operation.state, RetryCount: operation.retryCount, Error: operation.err})
	}
	return pending
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) run() {
	defer close(m.done)
	for {
		if err := m.waitUntilConnectable(); err != nil {
			m.finish()
			return
		}
		if !m.prepareAttempt() {
			continue
		}
		if m.ctx.Err() != nil {
			m.finish()
			return
		}
		connection, err := m.connect()
		if err != nil {
			if !m.handleConnectFailure(err) {
				continue
			}
			m.waitBackoff()
			continue
		}
		err = m.serve(connection)
		_ = connection.Close()
		m.clearConnection(connection, err)
		if m.ctx.Err() != nil {
			m.finish()
			return
		}
		m.mu.Lock()
		retry := m.automaticReconnect
		m.mu.Unlock()
		if retry {
			m.waitBackoff()
		}
	}
}

func (m *Manager) waitUntilConnectable() error {
	for {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		ready := m.docID != "" && m.automaticReconnect
		m.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-m.wake:
		}
	}
}

func (m *Manager) prepareAttempt() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.automaticReconnect || m.docID == "" {
		return false
	}
	if m.reconnectAttempts > 0 {
		m.setStateLocked(StateReconnecting)
	} else {
		m.setStateLocked(StateConnecting)
	}
	return true
}

func (m *Manager) connect() (Connection, error) {
	if m.config.Tokens.RefreshDueIn() <= 0 {
		m.setState(StateRefreshingToken)
	}
	tokenCtx, tokenCancel := context.WithTimeout(m.ctx, m.config.RefreshTimeout)
	token, err := m.config.Tokens.AccessToken(tokenCtx)
	tokenCancel()
	if err != nil {
		return nil, fmt.Errorf("prepare access token: %w", err)
	}
	visited := make(map[string]struct{})
	for redirects := 0; ; redirects++ {
		endpoint := m.endpoint()
		if endpoint != "" {
			visited[endpoint] = struct{}{}
		}
		m.setState(StateConnecting)
		connection, err := m.dial(endpoint)
		if err != nil {
			m.fallbackToSeed(endpoint)
			return nil, err
		}
		err = m.authenticateAndStart(connection, token)
		var redirect *OwnerRedirectError
		if !errors.As(err, &redirect) {
			if err != nil {
				var protocolError *quictransport.ProtocolError
				if errors.As(err, &protocolError) && protocolError.Code == protocol.CodeServiceUnavailable {
					// The old owner might still be reachable but fenced, or the new owner
					// might still be recovering. Return to the trusted seed for resolution.
					m.fallbackToSeed(endpoint)
				}
				_ = connection.Close()
				return nil, err
			}
			m.markConnected(connection)
			slog.Info("QUIC client connected", "component", "client", "event", "quic_reconnected", "endpoint", endpoint)
			return connection, nil
		}
		_ = connection.Close()
		if err := m.followRedirect(redirect.Endpoint, redirects, visited); err != nil {
			return nil, err
		}
	}
}

func (m *Manager) authenticateAndStart(connection Connection, token string) error {
	m.setConnection(connection)
	m.setState(StateAuthenticating)
	if err := connection.Authenticate(token); err != nil {
		_ = connection.Close()
		return fmt.Errorf("authenticate: %w", err)
	}
	m.mu.Lock()
	m.authenticated = true
	m.updatedAt = time.Now().UTC()
	m.mu.Unlock()
	m.setState(StateSyncing)
	return m.startSession(connection)
}

func (m *Manager) markConnected(connection Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connection = connection
	m.reconnectAttempts = 0
	m.authRefreshAttempted = false
	m.lastDisconnectReason = ""
	m.setStateLocked(StateConnected)
}

func (m *Manager) followRedirect(endpoint string, redirects int, visited map[string]struct{}) error {
	if redirects >= m.config.MaxRedirects {
		return ErrRedirectLoop
	}
	if err := validateRedirectEndpoint(endpoint); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRedirect, err)
	}
	if _, exists := visited[endpoint]; exists {
		return ErrRedirectLoop
	}
	m.mu.Lock()
	m.currentEndpoint = endpoint
	m.authenticated = false
	m.updatedAt = time.Now().UTC()
	m.mu.Unlock()
	return nil
}

type OwnerRedirectError struct {
	DocumentID string
	Endpoint   string
}

func (err *OwnerRedirectError) Error() string {
	return fmt.Sprintf("document %q is owned by %s", err.DocumentID, err.Endpoint)
}

func (m *Manager) dial(endpoint string) (Connection, error) {
	dialCtx, cancel := context.WithTimeout(m.ctx, m.config.ConnectTimeout)
	defer cancel()
	if m.config.DialEndpoint != nil {
		return m.config.DialEndpoint(dialCtx, endpoint)
	}
	return m.config.Dial(dialCtx)
}

func (m *Manager) endpoint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentEndpoint
}

func (m *Manager) fallbackToSeed(failedEndpoint string) {
	if m.config.SeedEndpoint == "" || failedEndpoint == m.config.SeedEndpoint {
		return
	}
	m.mu.Lock()
	m.currentEndpoint = m.config.SeedEndpoint
	m.updatedAt = time.Now().UTC()
	m.mu.Unlock()
}

func validateRedirectEndpoint(endpoint string) error {
	if endpoint == "" || strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") {
		return errors.New("endpoint must be a host:port authority")
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return errors.New("endpoint must contain a valid host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("endpoint port is outside the valid range")
	}
	return nil
}

func (m *Manager) startSession(connection Connection) error {
	m.mu.Lock()
	message, resume := m.recoveryRequestLocked()
	m.mu.Unlock()
	if err := connection.Send(message); err != nil {
		return fmt.Errorf("send %s: %w", message.Type, err)
	}
	response, err := connection.Receive()
	if err != nil {
		return fmt.Errorf("receive recovery response: %w", err)
	}
	if response.Type == protocol.TypeError {
		return &quictransport.ProtocolError{Code: response.Code, Message: response.Message}
	}
	if response.Type == protocol.TypeOwnerRedirect {
		if response.DocID != m.docID {
			return fmt.Errorf("%w: redirect document does not match", ErrInvalidRedirect)
		}
		return &OwnerRedirectError{DocumentID: response.DocID, Endpoint: response.Endpoint}
	}
	if err := m.applyRecovery(response, resume); err != nil {
		return fmt.Errorf("%w: %v", ErrRecoveryValidation, err)
	}
	m.emitMessage(response)
	return nil
}

func (m *Manager) recoveryRequestLocked() (protocol.Message, bool) {
	message := protocol.Message{Type: protocol.TypeJoin, DocID: m.docID}
	if !m.hasDocument {
		return message, false
	}
	message.Type = protocol.TypeResume
	message.LastVersion = m.version
	return message, true
}

func (m *Manager) applyRecovery(message protocol.Message, resume bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recovered, err := m.recoveredDocumentLocked(message, resume)
	if err != nil {
		return err
	}
	if m.config.StateStore != nil {
		document := clientstorage.Document{DocID: m.docID, ServerVersion: recovered.version, Content: recovered.content}
		if err := m.config.StateStore.SaveDocument(context.Background(), document); err != nil {
			return fmt.Errorf("persist recovered document: %w", err)
		}
	}
	m.content, m.version, m.hasDocument = recovered.content, recovered.version, recovered.available
	for _, pending := range m.pending {
		if pending.state != PendingFailed {
			pending.state = PendingQueued
			pending.err = ""
		}
	}
	m.updatedAt = time.Now().UTC()
	return nil
}

type recoveredDocument struct {
	content   string
	version   uint64
	available bool
}

func (m *Manager) recoveredDocumentLocked(message protocol.Message, resume bool) (recoveredDocument, error) {
	recovered := recoveredDocument{content: m.content, version: m.version, available: m.hasDocument}
	switch message.Type {
	case protocol.TypeJoined:
		if resume || message.DocID != m.docID {
			return recoveredDocument{}, errors.New("unexpected JOINED response")
		}
		return recoveredDocument{content: message.Content, version: message.Version, available: true}, nil
	case protocol.TypeSync:
		if !resume || message.DocID != m.docID || message.FromVersion != m.version {
			return recoveredDocument{}, errors.New("SYNC does not start at the local version")
		}
		content, version, err := applyOperations(recovered.content, recovered.version, message.Operations, message.ToVersion)
		if err != nil {
			return recoveredDocument{}, err
		}
		return recoveredDocument{content: content, version: version, available: true}, nil
	case protocol.TypeResyncRequired:
		if !resume || message.DocID != m.docID || message.Snapshot == nil {
			return recoveredDocument{}, errors.New("invalid RESYNC_REQUIRED response")
		}
		content, version, err := applyOperations(message.Snapshot.Content, message.Snapshot.Version, message.Operations, message.CurrentVersion)
		if err != nil {
			return recoveredDocument{}, err
		}
		return recoveredDocument{content: content, version: version, available: true}, nil
	default:
		return recoveredDocument{}, fmt.Errorf("unexpected recovery response %q", message.Type)
	}
}

type receiveResult struct {
	message protocol.Message
	err     error
}

func (m *Manager) serve(connection Connection) error {
	reads, stopReceiver := m.startReceiver(connection)
	defer stopReceiver()
	refreshTimer := time.NewTimer(positiveDuration(m.config.Tokens.RefreshDueIn()))
	defer refreshTimer.Stop()
	if err := m.sendNextPending(connection); err != nil {
		return err
	}
	for {
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-m.wake:
			if err := m.handleWake(connection); err != nil {
				return err
			}
		case result := <-reads:
			if result.err != nil {
				return result.err
			}
			if err := m.handleMessage(result.message); err != nil {
				return err
			}
			if err := m.sendNextPending(connection); err != nil {
				return err
			}
		case <-refreshTimer.C:
			if err := m.refreshAccessToken(); err != nil {
				return err
			}
			refreshTimer.Reset(positiveDuration(m.config.Tokens.RefreshDueIn()))
		}
	}
}

func (m *Manager) startReceiver(connection Connection) (<-chan receiveResult, func()) {
	results := make(chan receiveResult, 1)
	readCtx, cancelRead := context.WithCancel(m.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			message, err := connection.Receive()
			select {
			case results <- receiveResult{message: message, err: err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	stop := func() {
		// Close the old connection before waiting for its sole reader, preventing
		// blocked read goroutines from accumulating across reconnects.
		cancelRead()
		_ = connection.Close()
		<-done
	}
	return results, stop
}

func (m *Manager) handleWake(connection Connection) error {
	m.mu.Lock()
	forceReconnect := m.forceReconnect
	automaticReconnect := m.automaticReconnect
	m.forceReconnect = false
	m.mu.Unlock()
	if forceReconnect {
		if !automaticReconnect {
			return ErrReconnectStopped
		}
		return errors.New("manual reconnect requested")
	}
	return m.sendNextPending(connection)
}

func (m *Manager) refreshAccessToken() error {
	m.setState(StateRefreshingToken)
	ctx, cancel := context.WithTimeout(m.ctx, m.config.RefreshTimeout)
	_, err := m.config.Tokens.ForceRefresh(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("refresh access token: %w", err)
	}
	m.setState(StateConnected)
	return nil
}

func (m *Manager) sendNextPending(connection Connection) error {
	m.mu.Lock()
	next := m.nextPendingLocked()
	if next == nil {
		m.mu.Unlock()
		return nil
	}
	next.retryCount++
	if m.config.StateStore != nil {
		if err := m.config.StateStore.UpdatePending(context.Background(), next.requestID, string(PendingSent), next.retryCount, ""); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("persist pending send state: %w", err)
		}
	}
	next.state = PendingSent
	message := protocol.Message{Type: protocol.TypeEdit, RequestID: next.requestID, DocID: m.docID, BaseVersion: next.operation.BaseVersion, Operation: operationCopy(next.operation)}
	m.mu.Unlock()
	if err := connection.Send(message); err != nil {
		m.mu.Lock()
		next.state = PendingQueued
		if m.config.StateStore != nil {
			_ = m.config.StateStore.UpdatePending(context.Background(), next.requestID, string(PendingQueued), next.retryCount, err.Error())
		}
		m.mu.Unlock()
		return err
	}
	return nil
}

func (m *Manager) nextPendingLocked() *pendingOperation {
	for _, operation := range m.pending {
		if operation.state == PendingSent {
			return nil
		}
		if operation.state == PendingQueued {
			return operation
		}
	}
	return nil
}

func (m *Manager) handleMessage(message protocol.Message) error {
	var err error
	switch message.Type {
	case protocol.TypeEditApplied:
		err = m.handleEditApplied(message)
	case protocol.TypeError:
		m.handleProtocolError(message)
	}
	if err != nil {
		return err
	}
	m.emitMessage(message)
	return nil
}

func (m *Manager) handleEditApplied(message protocol.Message) error {
	if message.Operation == nil {
		return errors.New("EDIT_APPLIED has no operation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	content, version := m.content, m.version
	if message.Version == m.version+1 {
		updated, err := collab.Apply(m.content, *message.Operation)
		if err != nil {
			return err
		}
		content, version = updated, message.Version
	} else if message.Version > m.version {
		return fmt.Errorf("received version gap: %d after %d", message.Version, m.version)
	}
	if m.config.StateStore != nil {
		document := clientstorage.Document{DocID: m.docID, ServerVersion: version, Content: content}
		if err := m.config.StateStore.CommitApplied(context.Background(), document, message.RequestID); err != nil {
			return fmt.Errorf("persist applied edit: %w", err)
		}
	}
	m.content, m.version = content, version
	if message.RequestID != "" {
		m.removePendingLocked(message.RequestID)
	}
	m.updatedAt = time.Now().UTC()
	return nil
}

func (m *Manager) handleProtocolError(message protocol.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pending := range m.pending {
		if pending.state != PendingSent {
			continue
		}
		pending.state = PendingFailed
		pending.err = message.Code + ": " + message.Message
		if m.config.StateStore != nil {
			_ = m.config.StateStore.UpdatePending(context.Background(), pending.requestID, string(PendingFailed), pending.retryCount, pending.err)
		}
		break
	}
	m.updatedAt = time.Now().UTC()
}

func (m *Manager) removePendingLocked(requestID string) {
	for index, pending := range m.pending {
		if pending.requestID == requestID {
			m.pending = append(m.pending[:index], m.pending[index+1:]...)
			return
		}
	}
}

func (m *Manager) handleConnectFailure(err error) bool {
	m.mu.Lock()
	m.connection = nil
	m.authenticated = false
	m.lastDisconnectReason = safeError(err)
	m.reconnectAttempts++
	m.resetSentPendingLocked()
	automatic := m.automaticReconnect
	m.setStateLocked(StateReconnecting)
	m.mu.Unlock()
	if !automatic {
		return false
	}
	var protocolError *quictransport.ProtocolError
	if errors.As(err, &protocolError) && protocolError.Code == protocol.CodePermissionDenied {
		m.mu.Lock()
		alreadyTried := m.authRefreshAttempted
		m.authRefreshAttempted = true
		m.mu.Unlock()
		if alreadyTried {
			m.stopReconnect(err)
			return false
		}
		m.setState(StateRefreshingToken)
		ctx, cancel := context.WithTimeout(m.ctx, m.config.RefreshTimeout)
		_, refreshErr := m.config.Tokens.ForceRefresh(ctx)
		cancel()
		if refreshErr != nil {
			m.stopReconnect(refreshErr)
			return false
		}
		return true
	}
	if errors.As(err, &protocolError) && protocolError.Code == protocol.CodeServiceUnavailable {
		return true
	}
	if errors.As(err, &protocolError) {
		m.stopReconnect(err)
		return false
	}
	if terminalConnectionError(err) {
		m.stopReconnect(err)
		return false
	}
	return true
}

func (m *Manager) waitBackoff() {
	m.mu.Lock()
	attempt := m.reconnectAttempts
	m.mu.Unlock()
	timer := time.NewTimer(m.config.Backoff.Duration(attempt))
	defer timer.Stop()
	select {
	case <-m.ctx.Done():
	case <-m.wake:
	case <-timer.C:
	}
}

func (m *Manager) stopReconnect(err error) {
	m.mu.Lock()
	m.automaticReconnect = false
	m.lastDisconnectReason = safeError(err)
	m.setStateLocked(StateDisconnected)
	m.mu.Unlock()
}

func (m *Manager) clearConnection(connection Connection, err error) {
	m.mu.Lock()
	if m.connection == connection {
		m.connection = nil
	}
	m.authenticated = false
	if err != nil && !errors.Is(err, context.Canceled) {
		m.lastDisconnectReason = safeError(err)
	}
	m.resetSentPendingLocked()
	if terminalConnectionError(err) {
		m.automaticReconnect = false
	}
	if m.automaticReconnect {
		m.reconnectAttempts++
		m.setStateLocked(StateReconnecting)
	} else {
		m.setStateLocked(StateDisconnected)
	}
	m.mu.Unlock()
}

func (m *Manager) resetSentPendingLocked() {
	for _, pending := range m.pending {
		if pending.state == PendingSent {
			pending.state = PendingQueued
		}
	}
}

func terminalConnectionError(err error) bool {
	return errors.Is(err, ErrRefreshRejected) ||
		errors.Is(err, ErrRefreshOutcomeUnknown) ||
		errors.Is(err, ErrInvalidAccessToken) ||
		errors.Is(err, ErrRecoveryValidation) ||
		errors.Is(err, ErrRedirectLoop) ||
		errors.Is(err, ErrInvalidRedirect)
}

func (m *Manager) setConnection(connection Connection) {
	m.mu.Lock()
	m.connection = connection
	m.mu.Unlock()
}

func (m *Manager) setState(state State) {
	m.mu.Lock()
	m.setStateLocked(state)
	m.mu.Unlock()
}

func (m *Manager) setStateLocked(state State) {
	m.state = state
	m.updatedAt = time.Now().UTC()
	callback := m.config.OnStateChange
	status := m.statusLocked()
	if callback != nil {
		go callback(status)
	}
}

func (m *Manager) emitMessage(message protocol.Message) {
	if callback := m.config.OnMessage; callback != nil {
		callback(message)
	}
}

func (m *Manager) finish() {
	m.mu.Lock()
	connection := m.connection
	m.connection = nil
	m.authenticated = false
	m.setStateLocked(StateClosed)
	m.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
}

func operationCopy(operation collab.Operation) *collab.Operation {
	copy := operation
	return &copy
}

func positiveDuration(duration time.Duration) time.Duration {
	if duration <= 0 {
		return time.Millisecond
	}
	return duration
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
