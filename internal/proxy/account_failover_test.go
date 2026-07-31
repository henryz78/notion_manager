package proxy

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyAccountAttemptErrorRetryableStatuses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "auth 401", err: errors.New("notion API error 401: unauthorized"), reason: "auth_error"},
		{name: "workspace auth 401", err: errors.New("loadUserContent API error 401: unauthorized"), reason: "auth_error"},
		{name: "quota auth 401", err: errors.New("V1 API error 401: unauthorized"), reason: "auth_error"},
		{name: "researcher auth 401", err: errors.New("notion researcher API error 401: unauthorized"), reason: "auth_error"},
		{name: "permission 403", err: errors.New("notion API error 403: forbidden"), reason: "permission_denied"},
		{name: "rate limit", err: errors.New("notion API error 429: rate limited"), reason: "rate_limited"},
		{name: "upstream unavailable", err: errors.New("notion API error 503: service unavailable"), reason: "upstream_5xx"},
		{name: "network", err: errors.New("send request: connection reset by peer"), reason: "network_error"},
		{name: "unstructured unauthorized", err: errors.New("upstream said unauthorized"), reason: "auth_suspected"},
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

func TestClassifyAccountAttemptErrorDoesNotRetryPromptTooLong(t *testing.T) {
	got := classifyAccountAttemptError(fmt.Errorf("%w: Prompt too long.", ErrPromptTooLong))
	if got.Retryable || got.Reason != "context_too_long" {
		t.Fatalf("prompt overflow classification = %+v", got)
	}
}

func TestInferenceAccountCallLimit(t *testing.T) {
	for poolSize, want := range map[int]int{-1: 0, 0: 0, 1: 1, 3: 3, 6: 3, 100: 3} {
		if got := inferenceAccountCallLimit(poolSize); got != want {
			t.Fatalf("pool size %d: limit=%d want=%d", poolSize, got, want)
		}
	}
}

func TestRequestSpecificFailuresDoNotQuarantineAccounts(t *testing.T) {
	for _, reason := range []string{"empty_response", "upstream_timeout", "context_too_long"} {
		if shouldQuarantineAccountFailure(reason) {
			t.Fatalf("%s must not affect later requests", reason)
		}
	}
	for _, reason := range []string{"auth_error", "auth_suspected", "permission_denied", "rate_limited", "upstream_5xx", "network_error"} {
		if !shouldQuarantineAccountFailure(reason) {
			t.Fatalf("%s should temporarily protect account selection", reason)
		}
	}
}

func TestPromptTooLongReturnsClientError(t *testing.T) {
	status, message, errorType := inferenceHTTPError(fmt.Errorf("%w: Prompt too long.", ErrPromptTooLong))
	if status != 400 || errorType != "invalid_request_error" || !strings.Contains(message, "context length exceeded") {
		t.Fatalf("status=%d type=%q message=%q", status, errorType, message)
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

func TestClassifyAccountAttemptErrorDoesNotRetryTimeout(t *testing.T) {
	got := classifyAccountAttemptError(errors.New("send request: context deadline exceeded"))
	if got.Retryable || got.Reason != "upstream_timeout" {
		t.Fatalf("timeout classification = %+v", got)
	}
}
