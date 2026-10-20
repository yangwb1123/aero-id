package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

const maxCallbackResponseHeaderBytes = int64(64 << 10)

func (s *callbackService) verifyRegistrationEndpoint(ctx context.Context, endpoint string) error {
	networkCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	if err := s.network.validateEndpoint(networkCtx, endpoint, s.cfg.AllowPrivateTarget); err != nil {
		return apperr.BadRequest("callback.endpoint_invalid", "callback endpoint is not allowed").Wrap(err)
	}
	challenge, err := randomChallenge()
	if err != nil {
		return apperr.Internal("callback.challenge_failed", "failed to create endpoint challenge").Wrap(err)
	}
	if err := s.verifyCallbackEndpoint(networkCtx, endpoint, challenge); err != nil || networkCtx.Err() != nil {
		return apperr.BadRequest("callback.verification_failed", "callback endpoint verification failed")
	}
	return nil
}

func randomChallenge() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// The caller owns the network-phase budget, including endpoint resolution,
// connection-time resolution, dial, TLS, headers and bounded response reads.
func (s *callbackService) verifyCallbackEndpoint(ctx context.Context, endpoint, challenge string) error {
	client, err := s.httpClient(ctx, endpoint)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(map[string]string{"type": "aero.callback.verify", "challenge": challenge})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, text/plain")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aero-Callback-Challenge", challenge)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return readCallbackChallenge(ctx, response, challenge, s.cfg.MaxResponseBytes)
}

func readCallbackChallenge(ctx context.Context, response *http.Response, challenge string, maxBytes int64) error {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("callback verification returned a non-success status")
	}
	value, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || int64(len(value)) > maxBytes {
		return errors.New("callback verification response is too large or unavailable")
	}
	return matchChallenge(value, challenge)
}

func matchChallenge(value []byte, challenge string) error {
	if strings.TrimSpace(string(value)) == challenge {
		return nil
	}
	var response struct {
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(value, &response) == nil && response.Challenge == challenge {
		return nil
	}
	var text string
	if json.Unmarshal(value, &text) == nil && text == challenge {
		return nil
	}
	return errors.New("callback verification challenge did not match")
}

func (s *callbackService) httpClient(ctx context.Context, endpoint string) (*http.Client, error) {
	client, err := newCallbackHTTPClient(ctx, endpoint, s.cfg, s.network)
	if err != nil {
		return nil, err
	}
	if s.roundTripper != nil {
		client.Transport = s.roundTripper
	}
	return client, nil
}

func newCallbackHTTPClient(ctx context.Context, endpoint string, cfg CallbackConfig, network callbackNetwork) (*http.Client, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("callback network deadline is required")
	}
	if err := network.validateEndpoint(ctx, endpoint, cfg.AllowPrivateTarget); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: network.boundDial(ctx, cfg.AllowPrivateTarget),
		DisableKeepAlives: true, TLSHandshakeTimeout: cfg.RequestTimeout,
		ResponseHeaderTimeout: cfg.RequestTimeout, MaxResponseHeaderBytes: maxCallbackResponseHeaderBytes,
	}
	return &http.Client{Timeout: cfg.RequestTimeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
