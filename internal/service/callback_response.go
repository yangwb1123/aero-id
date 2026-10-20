package service

import (
	"context"
	"errors"
	"io"
	"net/http"
)

func callbackDeliveryResponse(ctx context.Context, response *http.Response, maxBytes int64) (int, string, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if ctx.Err() != nil {
		return response.StatusCode, "", ctx.Err()
	}
	if err != nil {
		return response.StatusCode, "", err
	}
	if int64(len(data)) > maxBytes {
		return response.StatusCode, "", errors.New("callback response is too large")
	}
	return response.StatusCode, string(data), nil
}
