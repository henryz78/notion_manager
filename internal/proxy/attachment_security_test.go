package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractFileFromSourceRejectsLoopbackURL(t *testing.T) {
	_, err := extractFileFromSource(map[string]interface{}{
		"type": "url",
		"url":  "http://127.0.0.1/private",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("loopback URL error = %v, want private/reserved rejection", err)
	}
}

func TestExtractFileFromSourceRejectsOversizedRemoteAttachment(t *testing.T) {
	previousClient := remoteAttachmentHTTPClient
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "26214401")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	remoteAttachmentHTTPClient = server.Client()
	t.Cleanup(func() { remoteAttachmentHTTPClient = previousClient })

	_, err := extractFileFromSource(map[string]interface{}{
		"type": "url",
		"url":  server.URL + "/large",
	}, "document")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized URL error = %v, want size rejection", err)
	}
}

func TestExtractFileFromSourceRejectsNonSuccessStatus(t *testing.T) {
	previousClient := remoteAttachmentHTTPClient
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	remoteAttachmentHTTPClient = server.Client()
	t.Cleanup(func() { remoteAttachmentHTTPClient = previousClient })

	_, err := extractFileFromSource(map[string]interface{}{
		"type": "url",
		"url":  server.URL + "/missing",
	}, "image")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("non-success URL error = %v, want HTTP status rejection", err)
	}
}
