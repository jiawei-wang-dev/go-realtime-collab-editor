package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	NodeIDHeader      = "X-Collab-Node-ID"
	IncarnationHeader = "X-Collab-Node-Incarnation"
	permissionRPCPath = "/internal/v1/permissions"
	documentRPCPath   = "/internal/v1/documents/"
	maxResponseBytes  = 64 * 1024
)

type Client struct {
	local       cluster.Node
	directory   cluster.NodeDirectory
	credentials *Credentials
	timeout     time.Duration
}

type PermissionRequest struct {
	DocumentID string          `json:"document_id"`
	UserID     string          `json:"user_id"`
	Role       permission.Role `json:"role"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PermissionResult struct {
	DocumentID string          `json:"doc_id"`
	UserID     string          `json:"user_id"`
	Role       permission.Role `json:"role"`
}

func NewClient(local cluster.Node, directory cluster.NodeDirectory, credentials *Credentials, timeout time.Duration) (*Client, error) {
	if directory == nil || credentials == nil {
		return nil, errors.New("internal RPC node directory and TLS credentials are required")
	}
	if local.NodeID == "" || local.Incarnation == "" {
		return nil, errors.New("internal RPC local node identity is required")
	}
	if err := credentials.ValidateIdentity(local); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, errors.New("internal RPC timeout must be positive")
	}
	return &Client{local: local, directory: directory, credentials: credentials, timeout: timeout}, nil
}

func (client *Client) ForwardPermission(ctx context.Context, owner cluster.Owner, authorization, documentID, userID string, role permission.Role) error {
	if authorization == "" {
		return errors.New("forwarded authorization is required")
	}
	node, exists := client.directory.Lookup(owner.NodeID, owner.Incarnation)
	if !exists || !node.IsReady() {
		return errors.New("owner node is not present in trusted membership")
	}
	endpoint, serverName, err := trustedEndpoint(node.HTTPEndpoint)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(PermissionRequest{DocumentID: documentID, UserID: userID, Role: role})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPut, endpoint+permissionRPCPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build internal permission request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	otel.GetTextMapPropagator().Inject(request.Context(), propagation.HeaderCarrier(request.Header))
	response, err := client.do(request, serverName)
	if err != nil {
		return fmt.Errorf("call owner permission endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return nil
	}
	return decodeRemoteError(response, "owner node rejected the permission request")
}

func (client *Client) ForwardDocument(ctx context.Context, owner cluster.Owner, authorization, documentID string) (collab.Document, error) {
	var document collab.Document
	response, err := client.get(ctx, owner, authorization, documentRPCPath+url.PathEscape(documentID))
	if err != nil {
		return document, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return document, decodeRemoteError(response, "owner node rejected the document request")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&document); err != nil {
		return collab.Document{}, errors.New("owner node returned an invalid document response")
	}
	return document, nil
}

func (client *Client) ForwardGetPermission(ctx context.Context, owner cluster.Owner, authorization, documentID, userID string) (permission.Role, error) {
	path := documentRPCPath + url.PathEscape(documentID) + "/permissions/" + url.PathEscape(userID)
	response, err := client.get(ctx, owner, authorization, path)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", decodeRemoteError(response, "owner node rejected the permission query")
	}
	var result PermissionResult
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&result); err != nil || !result.Role.Valid() {
		return "", errors.New("owner node returned an invalid permission response")
	}
	return result.Role, nil
}

func (client *Client) get(ctx context.Context, owner cluster.Owner, authorization, path string) (*http.Response, error) {
	if authorization == "" {
		return nil, errors.New("forwarded authorization is required")
	}
	node, exists := client.directory.Lookup(owner.NodeID, owner.Incarnation)
	if !exists || !node.IsReady() {
		return nil, errors.New("owner node is not present in trusted membership")
	}
	endpoint, serverName, err := trustedEndpoint(node.HTTPEndpoint)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.timeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build internal read request: %w", err)
	}
	request.Header.Set("Authorization", authorization)
	otel.GetTextMapPropagator().Inject(request.Context(), propagation.HeaderCarrier(request.Header))
	response, err := client.do(request, serverName)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("call owner read endpoint: %w", err)
	}
	response.Body = &cancelOnCloseReadCloser{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func (client *Client) do(request *http.Request, serverName string) (*http.Response, error) {
	transport := &http.Transport{TLSClientConfig: client.credentials.ClientConfig(serverName)}
	httpClient := &http.Client{Transport: transport}
	response, err := httpClient.Do(request)
	transport.CloseIdleConnections()
	return response, err
}

func decodeRemoteError(response *http.Response, fallback string) error {
	var remote errorResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(&remote); err != nil || remote.Code == "" {
		return &cluster.RemoteError{Status: response.StatusCode, Code: "INTERNAL_ERROR", Message: fallback}
	}
	return &cluster.RemoteError{Status: response.StatusCode, Code: remote.Code, Message: remote.Message}
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnCloseReadCloser) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func trustedEndpoint(raw string) (endpoint, serverName string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("trusted membership contains an invalid internal HTTPS endpoint")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", "", errors.New("internal HTTPS endpoint must not contain a path")
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" || port == "" || strings.ContainsAny(host, "/?#@") {
		return "", "", errors.New("internal HTTPS endpoint must contain a host and port")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", "", errors.New("internal HTTPS endpoint has an invalid port")
	}
	return "https://" + parsed.Host, host, nil
}
