package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func randomChallenge() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func verifyCallbackEndpoint(ctx context.Context, endpoint, challenge string, cfg CallbackConfig) error {
	client, err := newCallbackHTTPClient(endpoint, cfg)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"type": "aero.callback.verify", "challenge": challenge})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
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
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("callback verification returned a non-success status")
	}
	value, err := io.ReadAll(io.LimitReader(response.Body, cfg.MaxResponseBytes+1))
	if err != nil || int64(len(value)) > cfg.MaxResponseBytes {
		return errors.New("callback verification response is too large")
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

func newCallbackHTTPClient(endpoint string, cfg CallbackConfig) (*http.Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid callback endpoint")
	}
	if err := validateCallbackEndpoint(endpoint, cfg.AllowPrivateTarget); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialCallbackTarget(ctx, network, address, cfg.AllowPrivateTarget)
		},
	}
	return &http.Client{Timeout: cfg.RequestTimeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func dialCallbackTarget(ctx context.Context, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("callback target cannot be resolved")
	}
	if !allowPrivate && !publicIPsOnly(ips) {
		return nil, errors.New("callback target resolved to a private address")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		if !allowPrivate && !publicIP(ip) {
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	return nil, err
}

func publicIPsOnly(ips []net.IP) bool {
	for _, ip := range ips {
		if !publicIP(ip) {
			return false
		}
	}
	return true
}

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}
