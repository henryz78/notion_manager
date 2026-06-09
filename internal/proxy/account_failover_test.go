package proxy

import (
	"errors"
	"testing"
)

func TestClassifyAccountAttemptErrorRetryableStatuses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "auth 401", err: errors.New("notion API error 401: unauthorized"), reason: "auth_error"},
		{name: "auth 403", err: errors.New("notion API error 403: forbidden"), reason: "auth_error"},
		{name: "rate limit", err: errors.New("notion API error 429: rate limited"), reason: "rate_limited"},
		{name: "upstream unavailable", err: errors.New("notion API error 503: service unavailable"), reason: "upstream_5xx"},
		{name: "network", err: errors.New("send request: context deadline exceeded"), reason: "network_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAccountAttemptError(tt.err)
			if !got.Retryable {
				t.Fatalf("expected retryable classification for %v", tt.err)
			}
			if got.Reason != tt.reason {
				t.Fatalf("reason=%q want %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestClassifyAccountAttemptErrorDoesNotRetryGlobalBadRequest(t *testing.T) {
	got := classifyAccountAttemptError(errors.New("notion API error 400: invalid model"))
	if got.Retryable {
		t.Fatalf("400 invalid model should not be account-retryable: %+v", got)
	}
	if got.Reason != "api_error" {
		t.Fatalf("reason=%q want api_error", got.Reason)
	}
}
