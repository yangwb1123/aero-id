package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func callbackDeliveryRequest(ctx context.Context, delivery *domain.CallbackDelivery, secret []byte) (*http.Request, error) {
	body, err := json.Marshal(delivery.Payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aero-Callback-Schema-Version", "1")
	request.Header.Set("X-Aero-Callback-Event-ID", delivery.EventID.String())
	request.Header.Set("X-Aero-Callback-Attempt", fmt.Sprintf("%d", delivery.Attempt+1))
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	request.Header.Set("X-Aero-Callback-Timestamp", timestamp)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp + "\n"))
	_, _ = mac.Write(body)
	request.Header.Set("X-Aero-Callback-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request, nil
}
