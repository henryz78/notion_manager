package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Session binds one client conversation to the real Notion thread that owns
// its server-generated Agent replies. Reusing this thread is required because
// current Notion ignores synthetic assistant-reply entries in a fresh replay.
type Session struct {
	mu                sync.Mutex
	managerKey        string
	managerEpoch      uint64
	managerKeyVersion uint64
	publishAssistant  func(ChatMessage)
	expectedClientKey string
	ThreadID          string
	AccountEmail      string
	ConfigID          string
	ContextID         string
	OriginalDatetime  string
	ModelUsed         string
	TurnCount         int
	RawMessageCount   int
	UpdatedConfigIDs  []string
	CreatedAt         time.Time
	LastUsedAt        time.Time
}

type SessionManager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	ambiguous   map[string]time.Time
	keyVersions map[string]uint64
	epoch       uint64
	ttl         time.Duration
	operations  atomic.Uint64
	sweeping    atomic.Bool
}

var globalSessionManager = NewSessionManager(2 * time.Hour)

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions:    make(map[string]*Session),
		ambiguous:   make(map[string]time.Time),
		keyVersions: make(map[string]uint64),
		epoch:       1,
		ttl:         ttl,
	}
}

func (session *Session) lockForRequest() {
	if session != nil {
		session.mu.Lock()
	}
}

func (session *Session) unlockForRequest() {
	if session != nil {
		session.mu.Unlock()
	}
}

func (session *Session) lastUsedAtSnapshot() time.Time {
	if session == nil {
		return time.Time{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.LastUsedAt
}

func (sm *SessionManager) ambiguousLocked(key string, now time.Time) bool {
	until, ok := sm.ambiguous[key]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(sm.ambiguous, key)
	return false
}

func (sm *SessionManager) Get(key string) *Session {
	if key == "" {
		return nil
	}
	sm.maybeSweepExpired()
	sm.mu.Lock()
	if sm.ambiguousLocked(key, time.Now()) {
		sm.mu.Unlock()
		return nil
	}
	session := sm.sessions[key]
	sm.mu.Unlock()
	if session == nil {
		return nil
	}
	if time.Since(session.lastUsedAtSnapshot()) <= sm.ttl {
		return session
	}
	sm.DeleteIf(key, session)
	return nil
}

func (sm *SessionManager) Set(key string, session *Session) {
	if key == "" || session == nil {
		return
	}
	sm.maybeSweepExpired()
	sm.mu.Lock()
	if sm.ambiguousLocked(key, time.Now()) {
		sm.mu.Unlock()
		return
	}
	if session.managerKey == "" {
		// Direct Set is used by tests and administrative helpers. Normal
		// inference sessions are bound when they are created.
		session.managerKey = key
		session.managerEpoch = sm.epoch
		session.managerKeyVersion = sm.keyVersions[key]
	} else if session.managerKey != key ||
		session.managerEpoch != sm.epoch ||
		session.managerKeyVersion != sm.keyVersions[key] {
		// Clear/Delete may have happened while this request was in flight.
		// Never let a stale completion resurrect an invalidated thread.
		sm.mu.Unlock()
		return
	}
	sm.sessions[key] = session
	sm.mu.Unlock()
}

func (sm *SessionManager) maybeSweepExpired() {
	if sm == nil || sm.operations.Add(1)%256 != 0 || !sm.sweeping.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer sm.sweeping.Store(false)
		sm.sweepExpired()
	}()
}

func (sm *SessionManager) sweepExpired() {
	now := time.Now()
	sm.mu.Lock()
	snapshot := make(map[string]*Session, len(sm.sessions))
	for key, session := range sm.sessions {
		snapshot[key] = session
	}
	for key, until := range sm.ambiguous {
		if !now.Before(until) {
			delete(sm.ambiguous, key)
		}
	}
	sm.mu.Unlock()

	for key, session := range snapshot {
		if now.Sub(session.lastUsedAtSnapshot()) > sm.ttl {
			sm.DeleteIf(key, session)
		}
	}

	sm.mu.Lock()
	for key := range sm.keyVersions {
		if sm.sessions[key] == nil {
			if _, ambiguous := sm.ambiguous[key]; !ambiguous {
				delete(sm.keyVersions, key)
			}
		}
	}
	sm.mu.Unlock()
}

// MarkAmbiguous disables thread reuse for a key for one TTL window. This is
// used when concurrent, duplicate, or rolled-back requests make it impossible
// to prove which server reply belongs to the client's next turn.
func (sm *SessionManager) MarkAmbiguous(key string) {
	if key == "" {
		return
	}
	sm.mu.Lock()
	delete(sm.sessions, key)
	sm.keyVersions[key]++
	sm.ambiguous[key] = time.Now().Add(sm.ttl)
	sm.mu.Unlock()
}

func (sm *SessionManager) Delete(key string) {
	if key == "" {
		return
	}
	sm.mu.Lock()
	delete(sm.sessions, key)
	sm.keyVersions[key]++
	sm.mu.Unlock()
}

// DeleteIf invalidates key only when it still points at expected. Callers use
// this while holding expected.mu so a failed request is removed before any
// waiter can continue the same possibly-mutated Notion thread.
func (sm *SessionManager) DeleteIf(key string, expected *Session) bool {
	if key == "" || expected == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.sessions[key] != expected {
		return false
	}
	delete(sm.sessions, key)
	sm.keyVersions[key]++
	return true
}

// PublishReplacement atomically moves an unsalted conversation binding from
// the prior assistant-reply key to the reply produced by the current request.
// The caller holds session.mu, so a fast next turn can discover the new key
// but cannot use the thread until the current request has fully completed.
func (sm *SessionManager) PublishReplacement(oldKey, newKey string, session *Session) bool {
	if newKey == "" || session == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	if sm.ambiguousLocked(newKey, now) {
		if oldKey != "" && oldKey != newKey && sm.sessions[oldKey] == session {
			delete(sm.sessions, oldKey)
			sm.keyVersions[oldKey]++
			sm.ambiguous[oldKey] = now.Add(sm.ttl)
		}
		return false
	}
	if oldKey != "" {
		if sm.sessions[oldKey] != session ||
			session.managerKey != oldKey ||
			session.managerEpoch != sm.epoch ||
			session.managerKeyVersion != sm.keyVersions[oldKey] {
			return false
		}
	}
	if existing := sm.sessions[newKey]; existing != nil && existing != session {
		// Two independent chats produced the same assistant signature. Refuse
		// to guess: invalidate the shared key so neither can see the other.
		delete(sm.sessions, newKey)
		sm.keyVersions[newKey]++
		sm.ambiguous[newKey] = now.Add(sm.ttl)
		if oldKey != "" && oldKey != newKey && sm.sessions[oldKey] == session {
			delete(sm.sessions, oldKey)
			sm.keyVersions[oldKey]++
			sm.ambiguous[oldKey] = now.Add(sm.ttl)
		}
		return false
	}

	if oldKey != "" && oldKey != newKey {
		delete(sm.sessions, oldKey)
		sm.keyVersions[oldKey]++
		sm.ambiguous[oldKey] = now.Add(sm.ttl)
	}
	session.managerKey = newKey
	session.managerEpoch = sm.epoch
	session.managerKeyVersion = sm.keyVersions[newKey]
	sm.sessions[newKey] = session
	return true
}

func (sm *SessionManager) DeleteByAccount(email string) {
	sm.mu.Lock()
	for key, session := range sm.sessions {
		if session.AccountEmail == email {
			delete(sm.sessions, key)
			sm.keyVersions[key]++
		}
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) Clear() {
	sm.mu.Lock()
	clear(sm.sessions)
	clear(sm.ambiguous)
	clear(sm.keyVersions)
	sm.epoch++
	sm.mu.Unlock()
}

func (sm *SessionManager) newSession(key, accountEmail string) *Session {
	session := newConversationSession(accountEmail)
	sm.mu.Lock()
	session.managerKey = key
	session.managerEpoch = sm.epoch
	session.managerKeyVersion = sm.keyVersions[key]
	sm.mu.Unlock()
	return session
}

func (sm *SessionManager) isCurrent(key string, session *Session) bool {
	if key == "" || session == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return !sm.ambiguousLocked(key, time.Now()) &&
		sm.sessions[key] == session &&
		session.managerKey == key &&
		session.managerEpoch == sm.epoch &&
		session.managerKeyVersion == sm.keyVersions[key]
}

func normalizeSessionSystemContent(content string) string {
	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "x-anthropic-billing-header:") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func normalizeSessionUserContent(content string) string {
	return strings.TrimSpace(stripSystemReminders(content))
}

func isMeaningfulUserMessage(message ChatMessage) bool {
	return message.Role == "user" &&
		message.ToolCallID == "" &&
		normalizeSessionUserContent(message.Content) != ""
}

func shouldCountConversationMessage(message ChatMessage) bool {
	switch message.Role {
	case "system":
		return false
	case "user":
		return isMeaningfulUserMessage(message)
	case "assistant":
		return strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0
	case "tool":
		return strings.TrimSpace(message.Content) != "" ||
			message.ToolCallID != "" ||
			message.Name != ""
	default:
		return strings.TrimSpace(message.Content) != ""
	}
}

func countConversationMessages(messages []ChatMessage) int {
	count := 0
	for _, message := range messages {
		if shouldCountConversationMessage(message) {
			count++
		}
	}
	return count
}

func extractLastUserMessage(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if isMeaningfulUserMessage(messages[i]) {
			return normalizeSessionUserContent(messages[i].Content)
		}
	}
	return ""
}

// buildPartialContinuationContent serializes only the client messages that
// arrived after Notion's latest stored assistant turn. Tool results must be
// included even when the client omits tools on the continuation request or
// sets tool_choice=none.
func buildPartialContinuationContent(messages []ChatMessage) string {
	lastAssistant := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return extractLastUserMessage(messages)
	}

	tail := messages[lastAssistant+1:]
	if len(tail) == 1 && isMeaningfulUserMessage(tail[0]) {
		return normalizeSessionUserContent(tail[0].Content)
	}

	var content strings.Builder
	for _, message := range tail {
		switch message.Role {
		case "tool":
			if content.Len() > 0 {
				content.WriteString("\n\n")
			}
			content.WriteString("TOOL_RESULT")
			if message.Name != "" {
				content.WriteString(" name=")
				content.WriteString(message.Name)
			}
			if message.ToolCallID != "" {
				content.WriteString(" tool_call_id=")
				content.WriteString(message.ToolCallID)
			}
			content.WriteString(":\n")
			content.WriteString(message.Content)
		case "user":
			userContent := normalizeSessionUserContent(message.Content)
			if message.ToolCallID != "" || userContent == "" {
				continue
			}
			if content.Len() > 0 {
				content.WriteString("\n\n")
			}
			content.WriteString("USER:\n")
			content.WriteString(userContent)
		}
	}
	if content.Len() == 0 {
		return extractLastUserMessage(messages)
	}
	return "The client sent these new messages after your previous assistant turn. Treat the role labels as authoritative.\n\n" + content.String()
}

func canonicalToolArguments(raw string) string {
	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			return string(canonical)
		}
	}
	return strings.TrimSpace(raw)
}

func computeConversationContinuationKeyWithContext(messages []ChatMessage, attachments []FileAttachment) string {
	if len(messages) == 0 || messages[len(messages)-1].Role != "assistant" {
		return ""
	}
	type canonicalCall struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type canonicalMessage struct {
		Role       string          `json:"role"`
		Content    string          `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		Name       string          `json:"name"`
		ToolCalls  []canonicalCall `json:"tool_calls,omitempty"`
	}
	type canonicalAttachment struct {
		ContentType  string `json:"content_type"`
		MessageIndex int    `json:"message_index"`
		SHA256       string `json:"sha256"`
	}
	payload := struct {
		Version     int                   `json:"version"`
		Messages    []canonicalMessage    `json:"messages"`
		Attachments []canonicalAttachment `json:"attachments,omitempty"`
	}{Version: 2}
	for _, message := range messages {
		content := message.Content
		switch message.Role {
		case "system":
			content = normalizeSessionSystemContent(content)
		case "user":
			content = normalizeSessionUserContent(content)
		}
		item := canonicalMessage{
			Role:       message.Role,
			Content:    content,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, canonicalCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: canonicalToolArguments(call.Function.Arguments),
			})
		}
		payload.Messages = append(payload.Messages, item)
	}
	for _, attachment := range attachments {
		contentHash := sha256.Sum256(attachment.Data)
		payload.Attachments = append(payload.Attachments, canonicalAttachment{
			ContentType:  attachment.ContentType,
			MessageIndex: attachment.MessageIndex,
			SHA256:       hex.EncodeToString(contentHash[:]),
		})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:32]
}

func computeConversationContinuationKey(messages []ChatMessage) string {
	return computeConversationContinuationKeyWithContext(messages, nil)
}

func extractAssistantContinuationKeyWithContext(messages []ChatMessage, attachments []FileAttachment, maxAttachmentMessageIndex int) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		filteredAttachments := make([]FileAttachment, 0, len(attachments))
		for _, attachment := range attachments {
			if attachment.MessageIndex <= maxAttachmentMessageIndex {
				filteredAttachments = append(filteredAttachments, attachment)
			}
		}
		if key := computeConversationContinuationKeyWithContext(messages[:i+1], filteredAttachments); key != "" {
			return key
		}
	}
	return ""
}

func extractAssistantContinuationKey(messages []ChatMessage) string {
	return extractAssistantContinuationKeyWithContext(messages, nil, -1)
}

func (session *Session) publishAssistantContinuation(message ChatMessage) {
	if session == nil || session.publishAssistant == nil {
		return
	}
	session.publishAssistant(message)
	session.publishAssistant = nil
}

func computeStableSessionFingerprint(stableSalt string) string {
	hash := sha256.New()
	hash.Write([]byte("conversation:"))
	hash.Write([]byte(stableSalt))
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

func extractConversationSalt(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{"session_id", "conversation_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if raw, ok := metadata["user_id"]; ok {
		switch value := raw.(type) {
		case map[string]interface{}:
			for _, key := range []string{"session_id", "conversation_id"} {
				if id, ok := value[key].(string); ok && strings.TrimSpace(id) != "" {
					return strings.TrimSpace(id)
				}
			}
		case string:
			var decoded map[string]interface{}
			if json.Unmarshal([]byte(value), &decoded) == nil {
				for _, key := range []string{"session_id", "conversation_id"} {
					if id, ok := decoded[key].(string); ok && strings.TrimSpace(id) != "" {
						return strings.TrimSpace(id)
					}
				}
			}
		}
	}
	return ""
}

// lockConversationSessionForRequest returns a session locked for exclusive use
// by one inference attempt. A missing session is atomically pre-published
// before inference, closing the window where a fast next turn could create a
// second thread before the first response handler stores its binding.
func lockConversationSessionForRequest(
	sm *SessionManager,
	key string,
	session *Session,
	rawMessageCount int,
	accountEmail string,
	clientContinuationKey string,
) (current *Session, reused bool, cacheable bool) {
	current = session
	for {
		if current == nil {
			sm.mu.Lock()
			if sm.ambiguousLocked(key, time.Now()) {
				sm.mu.Unlock()
				current = newConversationSession(accountEmail)
				current.lockForRequest()
				return current, false, false
			}
			if existing := sm.sessions[key]; existing != nil {
				current = existing
				sm.mu.Unlock()
			} else {
				current = newConversationSession(accountEmail)
				current.managerKey = key
				current.managerEpoch = sm.epoch
				current.managerKeyVersion = sm.keyVersions[key]
				current.lockForRequest()
				sm.sessions[key] = current
				sm.mu.Unlock()
				return current, false, true
			}
		}

		current.lockForRequest()
		if !sm.isCurrent(key, current) {
			current.unlockForRequest()
			current = nil
			continue
		}
		if current.TurnCount > 0 && current.expectedClientKey != clientContinuationKey {
			// The client edited, branched, rolled back, or compacted its
			// history. The stable ID can locate a candidate, but only the exact
			// client-visible history chain may authorize continuation.
			sm.DeleteIf(key, current)
			current.unlockForRequest()
			current = nil
			continue
		}
		if rawMessageCount > current.RawMessageCount {
			return current, current.TurnCount > 0, true
		}

		// Duplicate/rollback requests cannot be associated with a single
		// server reply safely. Disable reuse for this stable key for one TTL
		// and serve the request through an isolated full replay.
		sm.MarkAmbiguous(key)
		current.unlockForRequest()
		current = newConversationSession(accountEmail)
		current.lockForRequest()
		return current, false, false
	}
}

func newConversationSession(accountEmail string) *Session {
	now := time.Now()
	return &Session{
		ThreadID:         generateUUIDv4(),
		AccountEmail:     accountEmail,
		ConfigID:         generateUUIDv4(),
		ContextID:        generateUUIDv4(),
		OriginalDatetime: now.Format(time.RFC3339Nano),
		CreatedAt:        now,
		LastUsedAt:       now,
	}
}

func completeConversationSession(session *Session, rawMessageCount int, model string) {
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	completeConversationSessionLocked(session, rawMessageCount, model)
}

func completeConversationSessionLocked(session *Session, rawMessageCount int, model string) {
	advanceConversationServerTurnLocked(session, model)
	session.RawMessageCount = rawMessageCount
}

func advanceConversationServerTurnLocked(session *Session, model string) {
	if session == nil {
		return
	}
	session.TurnCount++
	session.ModelUsed = model
	session.UpdatedConfigIDs = append(session.UpdatedConfigIDs, generateUUIDv4())
	session.LastUsedAt = time.Now()
}
