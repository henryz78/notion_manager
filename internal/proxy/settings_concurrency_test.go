package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingRequestBody struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (body *blockingRequestBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.release
	return 0, io.EOF
}

func TestSettingsUpdateWaitsForInFlightInferenceSnapshot(t *testing.T) {
	previousConfig := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previousConfig })

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  enable_web_search: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := &blockingRequestBody{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	inferenceRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	inferenceDone := make(chan struct{})
	go func() {
		defer close(inferenceDone)
		HandleAnthropicMessages(NewAccountPool()).ServeHTTP(httptest.NewRecorder(), inferenceRequest)
	}()
	<-body.entered

	settingsDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"enable_web_search":false}`))
		recorder := httptest.NewRecorder()
		HandleAdminSettings(configPath, NewDashboardAuth("", "")).ServeHTTP(recorder, request)
		settingsDone <- recorder
	}()

	select {
	case <-settingsDone:
		t.Fatal("settings update completed while an inference request still held its settings snapshot")
	case <-time.After(50 * time.Millisecond):
	}

	close(body.release)
	<-inferenceDone
	select {
	case recorder := <-settingsDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("settings update status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settings update did not resume after inference released its snapshot")
	}
}
