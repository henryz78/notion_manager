package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestUploadFileUsesInferenceThreadForUploadAndProcessing(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	var mu sync.Mutex
	var uploadRequest NotionUploadURLRequest
	var taskRequest NotionEnqueueTaskRequest

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getUploadFileUrlForAssistantChatTranscriptUpload":
			if err := json.NewDecoder(r.Body).Decode(&uploadRequest); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(NotionUploadURLResponse{
				URL:                 "attachment:attachment-id:notes.txt",
				SignedUploadPostURL: server.URL + "/s3",
				Fields:              map[string]string{},
			})
		case "/s3":
			w.WriteHeader(http.StatusNoContent)
		case "/enqueueTask":
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewDecoder(r.Body).Decode(&taskRequest); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"taskId":"task-1"}`))
		case "/getTasks":
			_, _ = w.Write([]byte(`{"results":[{"id":"task-1","state":"success"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	const threadID = "thread-used-by-inference"
	uploaded, err := UploadFileToNotion(
		&Account{
			UserID: "user", UserEmail: "user@example.com", SpaceID: "space",
			ClientVersion: DefaultClientVersion, TokenV2: "token",
		},
		&FileAttachment{Data: []byte("hello"), FileName: "notes.txt", ContentType: "text/plain"},
		threadID,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.SessionID != threadID {
		t.Fatalf("uploaded session=%q, want %q", uploaded.SessionID, threadID)
	}
	if uploadRequest.CreateThread {
		t.Fatal("continuation upload unexpectedly requested thread creation")
	}
	if uploadRequest.Pointer.Table != "thread" || uploadRequest.Pointer.ID != threadID {
		t.Fatalf("upload pointer=%#v, want thread %q", uploadRequest.Pointer, threadID)
	}
	mu.Lock()
	defer mu.Unlock()
	if taskRequest.Task.Request.AISessionPointer.Table != "thread" ||
		taskRequest.Task.Request.AISessionPointer.ID != threadID {
		t.Fatalf("processing pointer=%#v, want thread %q", taskRequest.Task.Request.AISessionPointer, threadID)
	}
}
