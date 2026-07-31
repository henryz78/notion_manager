package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type idleTimeoutRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f idleTimeoutRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type delayedChunkReadCloser struct {
	chunks [][]byte
	delay  time.Duration
}

func (r *delayedChunkReadCloser) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(p, chunk), nil
}

func (r *delayedChunkReadCloser) Close() error { return nil }

func TestIdleTimeoutAllowsLongContinuousResponse(t *testing.T) {
	body := &idleTimeoutReadCloser{
		body: &delayedChunkReadCloser{
			chunks: [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")},
			delay:  40 * time.Millisecond,
		},
		timeout: 100 * time.Millisecond,
	}
	started := time.Now()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcd" {
		t.Fatalf("data=%q", data)
	}
	if elapsed := time.Since(started); elapsed <= body.timeout {
		t.Fatalf("test did not exceed one idle window: %v", elapsed)
	}
}

func TestIdleTimeoutStopsSilentResponse(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &idleTimeoutReadCloser{body: reader, timeout: 30 * time.Millisecond}
	_, err := body.Read(make([]byte, 1))
	if !errors.Is(err, ErrInferenceIdleTimeout) {
		t.Fatalf("error=%v want ErrInferenceIdleTimeout", err)
	}
}

func TestIdleTimeoutClassifiesResponseHeaderTimeout(t *testing.T) {
	previousClientOverride := chromeHTTPClientForTest
	chromeHTTPClientForTest = func(time.Duration) *http.Client {
		return &http.Client{Transport: idleTimeoutRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
	}
	defer func() { chromeHTTPClientForTest = previousClientOverride }()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/inference", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = doChromeRequestWithIdleTimeout(req, 20*time.Millisecond)
	if !errors.Is(err, ErrInferenceIdleTimeout) {
		t.Fatalf("error=%v want ErrInferenceIdleTimeout", err)
	}
}
