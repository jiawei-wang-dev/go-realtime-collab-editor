package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	httpClient *http.Client
	token      string
}

func NewUnixClient(socketPath, token string, timeout time.Duration) (*Client, error) {
	if socketPath == "" || token == "" {
		return nil, errors.New("runner socket and token are required")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{
		httpClient: &http.Client{Transport: transport, Timeout: timeout},
		token:      token,
	}, nil
}

func (client *Client) Run(ctx context.Context, request Request) (Result, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return Result{}, fmt.Errorf("encode runner request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runner/v1/run", bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("create runner request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Result{}, fmt.Errorf("runner returned %s: %s", response.Status, bytes.TrimSpace(message))
	}
	var result Result
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode runner response: %w", err)
	}
	return result, nil
}
