package sso

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aero/aero-id/internal/sso/ssopb"
)

type ClientConfig struct {
	TokenCacheTTL time.Duration
	UserCacheTTL  time.Duration
}

type Client struct {
	rpc        ssopb.SSOClient
	tokenCache *TokenCache
	userCache  *UserCache
	stopFn     func()
}

// NewClient dials the SSO endpoint and returns a Client with caches running.
func NewClient(ctx context.Context, endpoint string, insecureConn bool, cfg ClientConfig) (*Client, error) {
	var opts []grpc.DialOption
	if insecureConn {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial sso: %w", err)
	}
	return NewClientFromConn(conn, cfg), nil
}

// NewClientFromConn lets tests inject a bufconn-backed *grpc.ClientConn.
func NewClientFromConn(conn *grpc.ClientConn, cfg ClientConfig) *Client {
	c := &Client{
		rpc:        ssopb.NewSSOClient(conn),
		tokenCache: NewTokenCache(cfg.TokenCacheTTL),
	}
	c.userCache = NewUserCache(cfg.UserCacheTTL, c.fetchUsers)
	stop := c.tokenCache.RunCleaner(30 * time.Second)
	c.stopFn = stop
	return c
}

func (c *Client) Close() {
	if c.stopFn != nil {
		c.stopFn()
	}
}

// ValidateToken returns the uid for a Bearer token, consulting the cache first.
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
	if uid, ok := c.tokenCache.Get(token); ok {
		return uid, nil
	}
	resp, err := c.rpc.ValidateToken(ctx, &ssopb.ValidateTokenRequest{Token: token})
	if err != nil {
		return "", fmt.Errorf("sso validate: %w", err)
	}
	var exp time.Time
	if resp.ExpiresAtUnix > 0 {
		exp = time.Unix(resp.ExpiresAtUnix, 0)
	}
	c.tokenCache.Set(token, resp.Uid, exp)
	return resp.Uid, nil
}

// GetUsers returns user info for the given uids, batching uncached entries.
func (c *Client) GetUsers(ctx context.Context, uids []string) (map[string]User, error) {
	return c.userCache.GetMany(ctx, uids)
}

func (c *Client) fetchUsers(ctx context.Context, uids []string) (map[string]User, error) {
	resp, err := c.rpc.GetUsers(ctx, &ssopb.GetUsersRequest{Uids: uids})
	if err != nil {
		return nil, fmt.Errorf("sso get users: %w", err)
	}
	out := make(map[string]User, len(resp.Users))
	for _, u := range resp.Users {
		out[u.Uid] = User{UID: u.Uid, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out, nil
}
