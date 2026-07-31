package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractConversationSaltAcceptsJSONWhitespace(t *testing.T) {
	metadata := map[string]interface{}{
		"user_id": `{ "session_id" : "conversation-123" }`,
	}
	if got := extractConversationSalt(metadata); got != "conversation-123" {
		t.Fatalf("extractConversationSalt() = %q, want conversation-123", got)
	}
}

func TestStableConversationSaltSurvivesPromptAndHistoryCompaction(t *testing.T) {
	const salt = "conversation-123"
	if got, want := computeStableSessionFingerprint(salt), computeStableSessionFingerprint(salt); got != want {
		t.Fatalf("stable conversation ID changed key after prompt/history rewrite: got %q want %q", got, want)
	}
	if got := computeStableSessionFingerprint("conversation-456"); got == computeStableSessionFingerprint(salt) {
		t.Fatal("different stable conversation IDs produced the same key")
	}
}

func TestUnsaltedContinuationKeyUsesFullHistoryThroughLastAssistant(t *testing.T) {
	reply := ChatMessage{Role: "assistant", Content: "OK"}
	firstChain := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "question A"},
		reply,
	}
	secondChain := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "question B"},
		reply,
	}
	firstKey := computeConversationContinuationKey(firstChain)
	if firstKey == "" {
		t.Fatal("full-history continuation key is empty")
	}
	if got := extractAssistantContinuationKey(append(cloneChatMessages(firstChain), ChatMessage{Role: "user", Content: "next"})); got != firstKey {
		t.Fatalf("incoming follow-up key=%q, want %q", got, firstKey)
	}
	if secondKey := computeConversationContinuationKey(secondChain); secondKey == firstKey {
		t.Fatal("same short assistant answer collided across different histories")
	}

	toolChain := []ChatMessage{
		{Role: "user", Content: "read file"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call-1",
			Function: ToolCallFunction{
				Name:      "Read",
				Arguments: `{"path":"a.txt"}`,
			},
		}}},
	}
	toolKey := computeConversationContinuationKey(toolChain)
	withResult := append(cloneChatMessages(toolChain), ChatMessage{
		Role: "tool", ToolCallID: "call-1", Name: "Read", Content: "contents",
	})
	if got := extractAssistantContinuationKey(withResult); got != toolKey {
		t.Fatalf("tool-result continuation key=%q, want %q", got, toolKey)
	}
}

func TestPartialContinuationContentIncludesToolResultsWithoutTools(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "old question that must not be replayed"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID:       "call-1",
			Function: ToolCallFunction{Name: "lookup", Arguments: `{"key":"alpha"}`},
		}}},
		{Role: "tool", ToolCallID: "call-1", Name: "lookup", Content: `{"value":42}`},
		{Role: "user", Content: "Use that result."},
	}

	content := buildPartialContinuationContent(messages)
	for _, expected := range []string{"TOOL_RESULT", "call-1", "lookup", `{"value":42}`, "Use that result."} {
		if !strings.Contains(content, expected) {
			t.Fatalf("partial continuation omitted %q: %s", expected, content)
		}
	}
	if strings.Contains(content, "old question that must not be replayed") {
		t.Fatalf("partial continuation replayed an old user turn: %s", content)
	}
}

func TestPartialContinuationContentKeepsPlainUserTurnUnwrapped(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	}
	if got := buildPartialContinuationContent(messages); got != "second" {
		t.Fatalf("plain continuation = %q, want second", got)
	}
}

func TestContinuationKeyRoundTripsToolCallsAndSeparatesAttachments(t *testing.T) {
	base := []ChatMessage{{Role: "user", Content: "inspect"}}
	response := continuationMessageFromBlocks([]AnthropicContentBlock{{
		Type:  "tool_use",
		ID:    "call-1",
		Name:  "Read",
		Input: json.RawMessage(`{"b":2,"a":1}`),
	}})
	serverKey := computeConversationContinuationKey(append(cloneChatMessages(base), response))

	converted, _, _ := convertAnthropicMessages(nil, []AnthropicMessage{
		{Role: "user", Content: "inspect"},
		{Role: "assistant", Content: []interface{}{map[string]interface{}{
			"type": "tool_use",
			"id":   "call-1",
			"name": "Read",
			"input": map[string]interface{}{
				"a": float64(1),
				"b": float64(2),
			},
		}}},
	})
	if clientKey := extractAssistantContinuationKey(converted); clientKey != serverKey {
		t.Fatalf("tool call round-trip key=%q, want %q", clientKey, serverKey)
	}

	chain := append(cloneChatMessages(base), ChatMessage{Role: "assistant", Content: "done"})
	first := []FileAttachment{{MessageIndex: 0, ContentType: "image/png", Data: []byte("image-a")}}
	second := []FileAttachment{{MessageIndex: 0, ContentType: "image/png", Data: []byte("image-b")}}
	if computeConversationContinuationKeyWithContext(chain, first) ==
		computeConversationContinuationKeyWithContext(chain, second) {
		t.Fatal("different attachment contents produced the same continuation key")
	}
}

func TestContinuationKeyPreservesOrdinaryXMLButIgnoresCommandMetadata(t *testing.T) {
	xmlAlpha := []ChatMessage{
		{Role: "user", Content: "<query>alpha</query>"},
		{Role: "assistant", Content: "same answer"},
	}
	xmlBeta := []ChatMessage{
		{Role: "user", Content: "<query>beta</query>"},
		{Role: "assistant", Content: "same answer"},
	}
	if computeConversationContinuationKey(xmlAlpha) == computeConversationContinuationKey(xmlBeta) {
		t.Fatal("ordinary XML user content was stripped from the continuation identity")
	}

	commandOne := []ChatMessage{
		{Role: "user", Content: "continue\n<command-name>/one</command-name>\n<command-message>first</command-message>\n<command-args>a</command-args>"},
		{Role: "assistant", Content: "same answer"},
	}
	commandTwo := []ChatMessage{
		{Role: "user", Content: "continue\n<command-name>/two</command-name>\n<command-message>second</command-message>\n<command-args>b</command-args>"},
		{Role: "assistant", Content: "same answer"},
	}
	if computeConversationContinuationKey(commandOne) != computeConversationContinuationKey(commandTwo) {
		t.Fatal("dynamic Claude Code command metadata changed the continuation identity")
	}
}

func TestNestedToolResultImageParticipatesInContinuationDigest(t *testing.T) {
	buildChain := func(imageData string) ([]ChatMessage, []FileAttachment) {
		t.Helper()
		converted, attachments, err := convertAnthropicMessages(nil, []AnthropicMessage{
			{Role: "user", Content: "inspect the screenshot"},
			{Role: "assistant", Content: []interface{}{map[string]interface{}{
				"type":  "tool_use",
				"id":    "call-image",
				"name":  "capture",
				"input": map[string]interface{}{},
			}}},
			{Role: "user", Content: []interface{}{map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "call-image",
				"content": []interface{}{map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": "image/png",
						"data":       base64.StdEncoding.EncodeToString([]byte(imageData)),
					},
				}},
			}}},
			{Role: "assistant", Content: "processed"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return converted, attachments
	}

	firstChain, firstAttachments := buildChain("image-a")
	secondChain, secondAttachments := buildChain("image-b")
	if len(firstAttachments) != 1 || firstAttachments[0].MessageIndex != 2 ||
		string(firstAttachments[0].Data) != "image-a" {
		t.Fatalf("nested tool-result image was not extracted correctly: %#v", firstAttachments)
	}
	foundToolResult := false
	for _, message := range firstChain {
		if message.Role == "tool" && message.ToolCallID == "call-image" &&
			message.Name == "capture" && message.Content == "[image attachment]" {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatalf("nested image tool result was not preserved in converted history: %#v", firstChain)
	}
	firstKey := computeConversationContinuationKeyWithContext(firstChain, firstAttachments)
	secondKey := computeConversationContinuationKeyWithContext(secondChain, secondAttachments)
	if firstKey == "" || secondKey == "" || firstKey == secondKey {
		t.Fatalf("nested tool-result image bytes did not affect digest: first=%q second=%q", firstKey, secondKey)
	}
}

func TestUnsaltedContinuationBindingMovesOnceAndRejectsCollision(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	first := newConversationSession("first@example.com")
	firstKey := "first-reply"
	secondKey := "second-reply"
	if !manager.PublishReplacement("", firstKey, first) {
		t.Fatal("failed to publish first unsalted reply")
	}
	if manager.Get(firstKey) != first {
		t.Fatal("first reply key did not resolve to its session")
	}
	if !manager.PublishReplacement(firstKey, secondKey, first) {
		t.Fatal("failed to atomically move continuation binding")
	}
	if manager.Get(firstKey) != nil {
		t.Fatal("consumed continuation key remained reusable for a branch")
	}
	if manager.Get(secondKey) != first {
		t.Fatal("new continuation key did not resolve to moved session")
	}

	colliding := newConversationSession("second@example.com")
	if manager.PublishReplacement("", secondKey, colliding) {
		t.Fatal("two independent sessions claimed the same continuation key")
	}
	if manager.Get(secondKey) != nil {
		t.Fatal("ambiguous continuation key still resolved to a session")
	}
}

func TestSessionIsPublishedBeforeFirstInferenceCompletes(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "fast-follow-up"

	first, reused, cacheable := lockConversationSessionForRequest(
		manager, key, nil, 1, "account@example.com", "",
	)
	if reused || !cacheable {
		t.Fatalf("first request reused=%v cacheable=%v, want false,true", reused, cacheable)
	}

	lookupStarted := make(chan struct{})
	lookupDone := make(chan *Session, 1)
	go func() {
		close(lookupStarted)
		lookupDone <- manager.Get(key)
	}()
	<-lookupStarted
	select {
	case <-lookupDone:
		t.Fatal("fast follow-up observed an unfinished session")
	case <-time.After(20 * time.Millisecond):
	}

	completeConversationSessionLocked(first, 1, "model")
	first.unlockForRequest()
	published := <-lookupDone
	if published != first {
		t.Fatalf("follow-up got session %#v, want first request session %#v", published, first)
	}

	current, reused, cacheable := lockConversationSessionForRequest(
		manager, key, published, 3, "account@example.com", "",
	)
	defer current.unlockForRequest()
	if current != first || !reused || !cacheable {
		t.Fatalf("follow-up current=%p reused=%v cacheable=%v, want first,true,true", current, reused, cacheable)
	}
}

func TestSessionManagerAmbiguousFingerprintStaysOnFullReplay(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "same-opening"
	first := newConversationSession("first@example.com")
	completeConversationSession(first, 3, "model")
	manager.Set(key, first)

	if got := manager.Get(key); got != first {
		t.Fatalf("initial session lookup = %#v, want first session", got)
	}
	manager.MarkAmbiguous(key)
	if got := manager.Get(key); got != nil {
		t.Fatalf("ambiguous fingerprint returned a session: %#v", got)
	}

	second := newConversationSession("second@example.com")
	manager.Set(key, second)
	if got := manager.Get(key); got != nil {
		t.Fatalf("ambiguous fingerprint accepted a replacement session: %#v", got)
	}
}

func TestStableSessionIDStillRejectsEditedOrCompactedHistory(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "stable-session-id"
	stored := newConversationSession("account@example.com")
	stored.expectedClientKey = "old-visible-history"
	completeConversationSession(stored, 5, "model")
	manager.Set(key, stored)

	current, reused, cacheable := lockConversationSessionForRequest(
		manager,
		key,
		stored,
		7,
		"account@example.com",
		"compacted-visible-history",
	)
	if current == stored || reused || !cacheable {
		current.unlockForRequest()
		t.Fatalf("edited history current=%p stored=%p reused=%v cacheable=%v, want fresh,false,true", current, stored, reused, cacheable)
	}
	current.unlockForRequest()
	if manager.Get(key) != current {
		t.Fatal("fresh replay session was not atomically rebound to the stable ID")
	}
}

func TestLockConversationSessionRechecksCountAfterWaiting(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "salted-session"
	session := newConversationSession("account@example.com")
	completeConversationSession(session, 1, "model")
	manager.Set(key, session)

	// Simulate request A holding the session while request B has already
	// selected the same pointer. A completes raw message count 3 first.
	session.lockForRequest()
	type result struct {
		current   *Session
		reused    bool
		cacheable bool
	}
	resultCh := make(chan result, 1)
	var started sync.WaitGroup
	started.Add(1)
	go func() {
		started.Done()
		current, reused, cacheable := lockConversationSessionForRequest(
			manager, key, session, 3, "account@example.com", "",
		)
		resultCh <- result{current: current, reused: reused, cacheable: cacheable}
	}()
	started.Wait()
	completeConversationSessionLocked(session, 3, "model")
	session.unlockForRequest()

	got := <-resultCh
	defer got.current.unlockForRequest()
	if got.reused {
		t.Fatal("duplicate raw message count reused the completed session")
	}
	if got.cacheable {
		t.Fatal("duplicate raw message count remained cacheable")
	}
	if got.current == session {
		t.Fatal("duplicate request did not fall back to a fresh replay session")
	}
	if manager.Get(key) != nil {
		t.Fatal("duplicate salted request should remove the stale session binding")
	}
}

func TestUnsaltedRollbackMarksFingerprintAmbiguous(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "unsalted-session"
	session := newConversationSession("account@example.com")
	completeConversationSession(session, 5, "model")
	manager.Set(key, session)

	current, reused, cacheable := lockConversationSessionForRequest(
		manager, key, session, 3, "account@example.com", "",
	)
	current.unlockForRequest()
	if reused {
		t.Fatal("rollback unexpectedly reused the old Notion thread")
	}
	if cacheable {
		t.Fatal("duplicate/rollback session must disable cache reuse")
	}
	if manager.Get(key) != nil {
		t.Fatal("unsalted rollback must keep the fingerprint on safe full replay")
	}
}

func TestSessionManagerClearRejectsInFlightStaleSet(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	key := "settings-change"
	stale := manager.newSession(key, "account@example.com")

	// Simulate a request that captured the cache generation before an admin
	// settings change invalidated all existing and in-flight thread bindings.
	manager.Clear()
	manager.Set(key, stale)
	if got := manager.Get(key); got != nil {
		t.Fatalf("session created before Clear was resurrected: %#v", got)
	}

	fresh := manager.newSession(key, "account@example.com")
	manager.Set(key, fresh)
	if got := manager.Get(key); got != fresh {
		t.Fatalf("fresh post-Clear session was not accepted: %#v", got)
	}
}

func TestSessionManagerSweepReclaimsExpiredBindingsAndVersions(t *testing.T) {
	manager := NewSessionManager(time.Minute)
	expired := newConversationSession("account@example.com")
	expired.LastUsedAt = time.Now().Add(-2 * time.Minute)
	manager.Set("expired", expired)
	manager.mu.Lock()
	manager.ambiguous["old-ambiguous"] = time.Now().Add(-time.Minute)
	manager.keyVersions["old-ambiguous"] = 3
	manager.mu.Unlock()

	manager.sweepExpired()
	if manager.Get("expired") != nil {
		t.Fatal("expired session survived sweep")
	}
	manager.mu.RLock()
	_, hasAmbiguous := manager.ambiguous["old-ambiguous"]
	_, hasExpiredVersion := manager.keyVersions["expired"]
	_, hasAmbiguousVersion := manager.keyVersions["old-ambiguous"]
	manager.mu.RUnlock()
	if hasAmbiguous || hasExpiredVersion || hasAmbiguousVersion {
		t.Fatalf("sweep leaked metadata: ambiguous=%v expiredVersion=%v ambiguousVersion=%v", hasAmbiguous, hasExpiredVersion, hasAmbiguousVersion)
	}
}
