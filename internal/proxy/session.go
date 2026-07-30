package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Session binds one client conversation to the real Notion thread that owns
// its server-generated Agent replies. Reusing this thread is required because
// current Notion ignores synthetic assistant-reply entries in a fresh replay.
type Session struct {
	ThreadID         string
	AccountEmail     string
	ConfigID         string
	ContextID        string
	OriginalDatetime string
	ModelUsed        string
	TurnCount        int
	RawMessageCount  int
	UpdatedConfigIDs []string
	CreatedAt        time.Time
	LastUsedAt       time.Time
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
}

var globalSessionManager = NewSessionManager(2 * time.Hour)

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
}

func (sm *SessionManager) Get(key string) *Session {
	if key == "" {
		return nil
	}
	sm.mu.RLock()
	session := sm.sessions[key]
	sm.mu.RUnlock()
	if session == nil {
		return nil
	}
	if time.Since(session.LastUsedAt) <= sm.ttl {
		return session
	}
	sm.Delete(key)
	return nil
}

func (sm *SessionManager) Set(key string, session *Session) {
	if key == "" || session == nil {
		return
	}
	sm.mu.Lock()
	sm.sessions[key] = session
	sm.mu.Unlock()
}

func (sm *SessionManager) Delete(key string) {
	if key == "" {
		return
	}
	sm.mu.Lock()
	delete(sm.sessions, key)
	sm.mu.Unlock()
}

func (sm *SessionManager) DeleteByAccount(email string) {
	sm.mu.Lock()
	for key, session := range sm.sessions {
		if session.AccountEmail == email {
			delete(sm.sessions, key)
		}
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) Clear() {
	sm.mu.Lock()
	clear(sm.sessions)
	sm.mu.Unlock()
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

func computeSessionFingerprintWithSalt(messages []ChatMessage, stableSalt string) string {
	hash := sha256.New()
	if stableSalt != "" {
		hash.Write([]byte("conversation:"))
		hash.Write([]byte(stableSalt))
		hash.Write([]byte{'\n'})
	}
	for _, message := range messages {
		if message.Role != "system" {
			continue
		}
		content := normalizeSessionSystemContent(message.Content)
		if len(content) > 512 {
			content = content[:512]
		}
		hash.Write([]byte("system:"))
		hash.Write([]byte(content))
		hash.Write([]byte{'\n'})
		break
	}
	for _, message := range messages {
		if !isMeaningfulUserMessage(message) {
			continue
		}
		content := normalizeSessionUserContent(message.Content)
		if len(content) > 512 {
			content = content[:512]
		}
		hash.Write([]byte("user:"))
		hash.Write([]byte(content))
		break
	}
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
			for _, key := range []string{`"session_id":"`, `"conversation_id":"`} {
				start := strings.Index(value, key)
				if start < 0 {
					continue
				}
				start += len(key)
				if end := strings.IndexByte(value[start:], '"'); end >= 0 {
					return strings.TrimSpace(value[start : start+end])
				}
			}
		}
	}
	return ""
}

func shouldStartFreshForAmbiguousSingleTurn(session *Session, rawMessageCount int, stableSalt string) bool {
	return session != nil &&
		strings.TrimSpace(stableSalt) == "" &&
		rawMessageCount == 1 &&
		session.RawMessageCount == 1
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
	session.TurnCount++
	session.RawMessageCount = rawMessageCount
	session.ModelUsed = model
	session.UpdatedConfigIDs = append(session.UpdatedConfigIDs, generateUUIDv4())
	session.LastUsedAt = time.Now()
}
