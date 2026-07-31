package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInferenceLeaseSharesLimitAcrossLoginWorkspaces(t *testing.T) {
	pool := newPool(
		&Account{UserID: "same-user", SpaceID: "space-a", UserEmail: "same@example.com", PlanType: "team"},
		&Account{UserID: "same-user", SpaceID: "space-b", UserEmail: "same@example.com", PlanType: "team"},
	)

	first, err := pool.NextBestLease(nil)
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	second, err := pool.NextBestLease(nil)
	if err != nil || second == nil {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
	if _, err := pool.NextBestLease(nil); !errors.Is(err, ErrAllNotionLoginsBusy) {
		t.Fatalf("third lease error = %v, want ErrAllNotionLoginsBusy", err)
	}

	first.Release()
	first.Release() // idempotent
	replacement, err := pool.NextBestLease(nil)
	if err != nil || replacement == nil {
		t.Fatalf("replacement lease = %#v, %v", replacement, err)
	}
	replacement.Release()
	second.Release()
}

func TestInferenceLeaseFallsBackToAnotherLogin(t *testing.T) {
	paidA := &Account{UserID: "paid-user", SpaceID: "paid-a", UserEmail: "paid@example.com", PlanType: "team"}
	paidB := &Account{UserID: "paid-user", SpaceID: "paid-b", UserEmail: "paid@example.com", PlanType: "team"}
	trial := &Account{UserID: "trial-user", SpaceID: "trial", UserEmail: "trial@example.com", PlanType: "personal"}
	pool := newPool(paidA, paidB, trial)

	first, _ := pool.LeaseAccount(paidA)
	second, _ := pool.LeaseAccount(paidB)
	if first == nil || second == nil {
		t.Fatal("expected two leases for the paid login")
	}
	defer first.Release()
	defer second.Release()

	fallback, err := pool.NextBestLease(nil)
	if err != nil || fallback == nil {
		t.Fatalf("fallback lease = %#v, %v", fallback, err)
	}
	defer fallback.Release()
	if fallback.Account() != trial {
		t.Fatalf("fallback account = %#v, want other login", fallback.Account())
	}
}

func TestInferenceLeaseAdmissionIsAtomic(t *testing.T) {
	pool := newPool(&Account{UserID: "one-user", SpaceID: "space", UserEmail: "one@example.com", PlanType: "team"})
	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	var admitted atomic.Int32
	var leasesMu sync.Mutex
	var leases []*AccountLease

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := pool.NextBestLease(nil)
			if err == nil && lease != nil {
				admitted.Add(1)
				leasesMu.Lock()
				leases = append(leases, lease)
				leasesMu.Unlock()
				return
			}
			if !errors.Is(err, ErrAllNotionLoginsBusy) {
				t.Errorf("unexpected lease result: lease=%#v err=%v", lease, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := admitted.Load(); got != maxConcurrentInferenceRequestsPerLogin {
		t.Fatalf("admitted = %d, want %d", got, maxConcurrentInferenceRequestsPerLogin)
	}
	for _, lease := range leases {
		lease.Release()
	}
}

func TestBusyLoginReturns429WithoutCallingNotion(t *testing.T) {
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "notion-test-model"})
	t.Cleanup(func() {
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
	})

	pool := newPool(&Account{UserID: "busy-user", SpaceID: "busy-space", UserEmail: "busy@example.com", PlanType: "team"})
	first, _ := pool.NextBestLease(nil)
	second, _ := pool.NextBestLease(nil)
	if first == nil || second == nil {
		t.Fatal("failed to occupy both inference slots")
	}
	defer first.Release()
	defer second.Release()

	anthropicBody := []byte(`{"model":"gpt-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	anthropicRecorder := httptest.NewRecorder()
	HandleAnthropicMessages(pool).ServeHTTP(
		anthropicRecorder,
		httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(anthropicBody)),
	)
	assertBusyResponse(t, anthropicRecorder)

	openAIBody := []byte(`{"model":"gpt-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	openAIRecorder := httptest.NewRecorder()
	HandleOpenAIChatCompletions(pool).ServeHTTP(
		openAIRecorder,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(openAIBody)),
	)
	assertBusyResponse(t, openAIRecorder)
}

func assertBusyResponse(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode busy response: %v", err)
	}
	if payload.Error.Type != "rate_limit_error" || payload.Error.Message == "" {
		t.Fatalf("unexpected busy response: %#v", payload.Error)
	}
}
