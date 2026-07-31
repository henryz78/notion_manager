package proxy

import (
	"errors"
	"strconv"
	"strings"
)

type accountAttemptFailure struct {
	Retryable bool
	Reason    string
}

func shouldQuarantineAccountFailure(reason string) bool {
	switch reason {
	case "empty_response", "upstream_timeout", "context_too_long":
		return false
	default:
		return true
	}
}

func classifyAccountAttemptError(err error) accountAttemptFailure {
	if err == nil {
		return accountAttemptFailure{}
	}
	if errors.Is(err, ErrEmptyResponse) {
		return accountAttemptFailure{Retryable: true, Reason: "empty_response"}
	}
	if errors.Is(err, ErrPromptTooLong) {
		return accountAttemptFailure{Retryable: false, Reason: "context_too_long"}
	}
	if errors.Is(err, ErrInferenceIdleTimeout) {
		return accountAttemptFailure{Retryable: false, Reason: "upstream_timeout"}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "timeout") {
		return accountAttemptFailure{Retryable: false, Reason: "upstream_timeout"}
	}
	if strings.Contains(msg, "send request:") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "unexpected eof") {
		return accountAttemptFailure{Retryable: true, Reason: "network_error"}
	}
	if code, ok := notionAPIStatusCode(msg); ok {
		switch {
		case code == 401:
			return accountAttemptFailure{Retryable: true, Reason: "auth_error"}
		case code == 403:
			return accountAttemptFailure{Retryable: true, Reason: "permission_denied"}
		case code == 429:
			return accountAttemptFailure{Retryable: true, Reason: "rate_limited"}
		case code >= 500 && code <= 599:
			return accountAttemptFailure{Retryable: true, Reason: "upstream_5xx"}
		default:
			return accountAttemptFailure{Retryable: false, Reason: "api_error"}
		}
	}
	if strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid token") {
		return accountAttemptFailure{Retryable: true, Reason: "auth_suspected"}
	}
	if strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "permission denied") {
		return accountAttemptFailure{Retryable: true, Reason: "permission_denied"}
	}
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") {
		return accountAttemptFailure{Retryable: true, Reason: "rate_limited"}
	}
	return accountAttemptFailure{Retryable: false, Reason: "api_error"}
}

func notionAPIStatusCode(msg string) (int, bool) {
	for _, marker := range []string{
		"notion api error ",
		"notion researcher api error ",
		"loadusercontent api error ",
		"v1 api error ",
		"api error ",
	} {
		idx := strings.Index(msg, marker)
		if idx < 0 {
			continue
		}
		rest := msg[idx+len(marker):]
		fields := strings.FieldsFunc(rest, func(r rune) bool {
			return r < '0' || r > '9'
		})
		if len(fields) == 0 {
			return 0, false
		}
		code, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, false
		}
		return code, true
	}
	return 0, false
}
