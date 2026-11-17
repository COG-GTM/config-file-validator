package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MaxRemoteSchemaSize is the largest remote schema body, in bytes, that will be downloaded.
const MaxRemoteSchemaSize = 10 << 20

// RemoteSchemaTimeout bounds a single remote schema download.
const RemoteSchemaTimeout = 30 * time.Second

// ErrRemoteSchemaTooLarge is returned when a remote schema exceeds MaxRemoteSchemaSize.
var ErrRemoteSchemaTooLarge = fmt.Errorf("remote schema exceeds %d bytes", MaxRemoteSchemaSize)

// FetchRemoteSchema downloads a JSON schema over HTTP(S) with a timeout and a
// size cap, and returns the body only if it is complete, valid JSON.
func FetchRemoteSchema(schemaURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), RemoteSchemaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, schemaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching schema: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching schema: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching schema: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteSchemaSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading schema: %w", err)
	}
	if len(body) > MaxRemoteSchemaSize {
		return nil, ErrRemoteSchemaTooLarge
	}
	if !json.Valid(body) {
		return nil, errors.New("fetching schema: response is not valid JSON")
	}
	return body, nil
}
