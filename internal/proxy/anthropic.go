package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var ErrToolBridgeNoTool = errors.New("tool bridge produced no usable tool action")

const maxInferenceAccountCalls = 3
const maxEmptyResponseAttempts = 2
const maxAnthropicRequestBodyBytes = 64 * 1024 * 1024

func mergeSequentialUsage(first, second *UsageInfo) *UsageInfo {
	if first == nil && second == nil {
		return nil
	}
	if first == nil {
		copy := *second
		return &copy
	}
	if second == nil {
		copy := *first
		return &copy
	}
	merged := &UsageInfo{
		PromptTokens:     max(first.PromptTokens, second.PromptTokens),
		CompletionTokens: first.CompletionTokens + second.CompletionTokens,
	}
	merged.TotalTokens = merged.PromptTokens + merged.CompletionTokens
	return merged
}

func inferenceAccountCallLimit(poolSize int) int {
	if poolSize <= 0 {
		return 0
	}
	return min(poolSize, maxInferenceAccountCalls)
}

// citationReplacer is a streaming state machine that replaces Notion's
// [^{{URL}}] and [^URL] citation markers with numbered references [N]
// as text deltas arrive. It buffers only when inside a potential citation.
type citationReplacer struct {
	urlToNum     map[string]int
	urls         []string
	buf          strings.Builder
	state        int // 0=normal, 1=saw [, 2=inside [^...
	knownURLs    *[]string
	knownDocs    *[]CitationCandidate
	toolCallURLs *map[string][]string
	context      string
}

func newCitationReplacer(knownURLs *[]string, knownDocs *[]CitationCandidate, toolCallURLs *map[string][]string) *citationReplacer {
	return &citationReplacer{
		urlToNum:     make(map[string]int),
		knownURLs:    knownURLs,
		knownDocs:    knownDocs,
		toolCallURLs: toolCallURLs,
	}
}

func trimCitationContext(text string) string {
	const maxRunes = 320
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[len(runes)-maxRunes:])
}

// Process takes a delta and returns text with citations replaced by [N].
// Partial citations are buffered across calls.
func (cr *citationReplacer) Process(delta string) string {
	var out strings.Builder
	for _, ch := range delta {
		switch cr.state {
		case 0: // normal text
			if ch == '[' {
				cr.state = 1
				cr.buf.Reset()
				cr.buf.WriteRune(ch)
			} else {
				out.WriteRune(ch)
			}
		case 1: // saw [, expecting ^
			if ch == '^' {
				cr.state = 2
				cr.buf.WriteRune(ch)
			} else {
				// Not a citation — flush buffered [ and continue
				out.WriteString(cr.buf.String())
				cr.buf.Reset()
				cr.state = 0
				if ch == '[' {
					cr.state = 1
					cr.buf.WriteRune(ch)
				} else {
					out.WriteRune(ch)
				}
			}
		case 2: // inside [^..., waiting for ]
			cr.buf.WriteRune(ch)
			if ch == ']' {
				// Complete citation — replace with [N]
				raw := cr.buf.String()
				matches := citationRe.FindStringSubmatch(raw)
				if len(matches) >= 2 {
					context := trimCitationContext(cr.context + out.String())
					rawURL, ok := normalizeCitationTargetWithContext(
						matches[1],
						cr.toolCallURLCandidates(),
						cr.knownURLCandidates(),
						cr.knownDocCandidates(),
						context,
					)
					if !ok {
						// Drop unresolved/non-URL citation tokens (e.g. toolu_* ids).
						cr.buf.Reset()
						cr.state = 0
						continue
					}
					num, exists := cr.urlToNum[rawURL]
					if !exists {
						num = len(cr.urls) + 1
						cr.urlToNum[rawURL] = num
						cr.urls = append(cr.urls, rawURL)
					}
					out.WriteString(fmt.Sprintf(" [%d]", num))
				} else {
					out.WriteString(raw) // not a valid citation, flush raw
				}
				cr.buf.Reset()
				cr.state = 0
			} else if cr.buf.Len() > 2000 {
				// Too long — incomplete citation, drop the markup
				cr.buf.Reset()
				cr.state = 0
			}
		}
	}
	produced := out.String()
	if produced != "" {
		cr.context = trimCitationContext(cr.context + produced)
	}
	return produced
}

// Flush returns any remaining buffered content.
// Incomplete citations (state 2: inside [^...) are dropped rather than
// flushing raw markup like [^{{URL... to the user.
func (cr *citationReplacer) Flush() string {
	var s string
	if cr.state < 2 {
		// State 0 or 1: flush buffered [ if any
		s = cr.buf.String()
	}
	// State 2: inside incomplete citation — drop it
	cr.buf.Reset()
	cr.state = 0
	if s != "" {
		cr.context = trimCitationContext(cr.context + s)
	}
	return s
}

// URLs returns the collected unique citation URLs in order of first appearance.
func (cr *citationReplacer) URLs() []string { return cr.urls }

func (cr *citationReplacer) knownURLCandidates() []string {
	if cr == nil || cr.knownURLs == nil {
		return nil
	}
	return *cr.knownURLs
}

func (cr *citationReplacer) knownDocCandidates() []CitationCandidate {
	if cr == nil || cr.knownDocs == nil {
		return nil
	}
	return *cr.knownDocs
}

func (cr *citationReplacer) toolCallURLCandidates() map[string][]string {
	if cr == nil || cr.toolCallURLs == nil {
		return nil
	}
	return *cr.toolCallURLs
}

func formatCitationSources(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	var sources strings.Builder
	sources.WriteString("\n---\nSources:\n")
	for i, u := range urls {
		sources.WriteString(fmt.Sprintf("[%d] %s\n", i+1, u))
	}
	return sources.String()
}

func renderAnthropicCitationText(rawText string, knownURLs []string, knownDocs []CitationCandidate, toolCallURLs map[string][]string) string {
	if rawText == "" {
		return ""
	}
	cr := newCitationReplacer(&knownURLs, &knownDocs, &toolCallURLs)
	var rendered strings.Builder
	rendered.WriteString(cr.Process(rawText))
	rendered.WriteString(cr.Flush())
	rendered.WriteString(formatCitationSources(cr.URLs()))
	return rendered.String()
}

// streamWebSearch streams web search results directly as SSE events.
// It replaces inline citations [^{{URL}}] with [N] in real-time using
// a buffered state machine, emits thinking blocks as they arrive,
// then appends a Sources section.
func streamWebSearch(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, acc *Account, query string, model string, requestID string, blockIndex *int, hasThinking bool, session *Session) (*UsageInfo, string, error) {
	var finalUsage *UsageInfo
	var thinkingBlocks []ThinkingBlock
	var streamedText strings.Builder
	var knownCitationURLs []string
	var knownCitationDocs []CitationCandidate
	knownToolCallURLs := make(map[string][]string)
	cr := newCitationReplacer(&knownCitationURLs, &knownCitationDocs, &knownToolCallURLs)
	textBlockStarted := false
	thinkingEmitted := 0

	messages := []ChatMessage{
		{Role: "user", Content: query},
	}
	callOpts := CallOptions{
		Context:                 ctx,
		EnableWebSearch:         true,
		ThinkingBlocks:          &thinkingBlocks,
		KnownCitationURLs:       &knownCitationURLs,
		KnownCitationDocs:       &knownCitationDocs,
		KnownToolCallURLs:       &knownToolCallURLs,
		Session:                 session,
		ForceThreadContinuation: session != nil,
		RequestID:               requestID,
	}
	if session != nil {
		// The bridge inference that selected WebSearch is already a completed
		// server turn. Record it before continuing the same Notion thread; a
		// later failure invalidates this session, so partial state is never reused.
		advanceConversationServerTurnLocked(session, model)
	}

	// emitPendingThinking emits any thinking blocks that have been collected
	// since the last check. Called before first text delta to ensure thinking
	// appears before text in the SSE stream.
	emitPendingThinking := func() {
		if !hasThinking {
			return
		}
		for thinkingEmitted < len(thinkingBlocks) {
			tb := thinkingBlocks[thinkingEmitted]
			sig := tb.Signature
			if sig == "" {
				sig = generateFakeSignature()
			}
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         *blockIndex,
				"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
			})
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": *blockIndex,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": tb.Content},
			})
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": *blockIndex,
				"delta": map[string]interface{}{"type": "signature_delta", "signature": sig},
			})
			sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
				"type": "content_block_stop", "index": *blockIndex,
			})
			*blockIndex++
			thinkingEmitted++
			log.Printf("[search-thinking] emitted thinking block %d (%d chars)", thinkingEmitted, len(tb.Content))
		}
	}

	// emitTextDelta starts text block lazily and sends text delta
	emitTextDelta := func(text string) {
		if text == "" {
			return
		}
		streamedText.WriteString(text)
		if !textBlockStarted {
			// Emit any pending thinking blocks before starting text
			emitPendingThinking()
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         *blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			textBlockStarted = true
		}
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": *blockIndex,
			"delta": map[string]interface{}{"type": "text_delta", "text": text},
		})
	}

	err := CallInference(acc, messages, model, false, func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			// Check for new thinking blocks on each text delta
			emitPendingThinking()
			emitTextDelta(cr.Process(delta))
		}
		if usage != nil {
			finalUsage = usage
		}
	}, callOpts)

	// Emit any remaining thinking blocks (e.g., if no text was produced)
	emitPendingThinking()

	// Flush any remaining buffered citation content
	emitTextDelta(cr.Flush())

	// Close text block if started
	if textBlockStarted {
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": *blockIndex,
		})
		*blockIndex++
	}

	// Append Sources section with numbered URLs
	if err == nil {
		urls := cr.URLs()
		if sourcesText := formatCitationSources(urls); sourcesText != "" {
			streamedText.WriteString(sourcesText)
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         *blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": *blockIndex,
				"delta": map[string]interface{}{"type": "text_delta", "text": sourcesText},
			})
			sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
				"type": "content_block_stop", "index": *blockIndex,
			})
			*blockIndex++
		}
	}

	var contentBlocks []AnthropicContentBlock
	if hasThinking {
		for _, tb := range thinkingBlocks {
			sig := tb.Signature
			if sig == "" {
				sig = generateFakeSignature()
			}
			contentBlocks = append(contentBlocks, AnthropicContentBlock{
				Type:      "thinking",
				Thinking:  tb.Content,
				Signature: sig,
			})
		}
	}
	if streamedText.Len() > 0 {
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type: "text",
			Text: streamedText.String(),
		})
	}
	if len(contentBlocks) > 0 {
		LogAPIOutputJSON(requestID, "anthropic stream web-search summary", map[string]interface{}{
			"model":   model,
			"query":   query,
			"content": contentBlocks,
		})
	}

	if err == nil && strings.TrimSpace(streamedText.String()) == "" {
		err = ErrEmptyResponse
	}
	return finalUsage, streamedText.String(), err
}

// ========== Anthropic Messages API Types ==========

// AnthropicRequest represents an Anthropic Messages API request
type AnthropicRequest struct {
	Model             string                 `json:"model"`
	MaxTokens         int                    `json:"max_tokens"`
	System            interface{}            `json:"system,omitempty"`
	Messages          []AnthropicMessage     `json:"messages"`
	Stream            bool                   `json:"stream"`
	Temperature       *float64               `json:"temperature,omitempty"`
	TopP              *float64               `json:"top_p,omitempty"`
	TopK              *int                   `json:"top_k,omitempty"`
	StopSequences     []string               `json:"stop_sequences,omitempty"`
	Tools             []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice        interface{}            `json:"tool_choice,omitempty"`
	Thinking          interface{}            `json:"thinking,omitempty"`
	OutputConfig      *AnthropicOutputConfig `json:"output_config,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	ContextManagement interface{}            `json:"context_management,omitempty"`
}

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []ContentBlock
}

// AnthropicContentBlock represents a content block in Anthropic format
type AnthropicContentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      interface{}     `json:"content,omitempty"`
	CacheControl interface{}     `json:"cache_control,omitempty"`
}

// AnthropicTool represents a tool definition in Anthropic format
type AnthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema,omitempty"`
}

type AnthropicOutputConfig struct {
	Format *AnthropicOutputFormat `json:"format,omitempty"`
	Effort string                 `json:"effort,omitempty"`
}

type AnthropicOutputFormat struct {
	Type   string      `json:"type,omitempty"`
	Schema interface{} `json:"schema,omitempty"`
}

// AnthropicResponse represents a non-streaming Anthropic Messages API response
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        *AnthropicUsage         `json:"usage"`
}

// AnthropicUsage represents token usage in Anthropic format
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

const anthropicJSONContentType = "application/json; charset=utf-8"

func lastAssistantAnthropicMessageIndex(messages []AnthropicMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return i
		}
	}
	return -1
}

func continuationMessageFromBlocks(blocks []AnthropicContentBlock) ChatMessage {
	message := ChatMessage{Role: "assistant"}
	var text strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}
	message.Content = text.String()
	return message
}

var structuredOutputLeadingTagRegex = regexp.MustCompile(`(?s)^\s*(?:<[A-Za-z][^>\n]*/>\s*)+`)

func isJSONSchemaOutput(outputConfig *AnthropicOutputConfig) bool {
	return outputConfig != nil && outputConfig.Format != nil && outputConfig.Format.Type == "json_schema" && outputConfig.Format.Schema != nil
}

func extractStructuredJSONObject(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimSpace(structuredOutputLeadingTagRegex.ReplaceAllString(trimmed, ""))
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed)) {
		return trimmed
	}
	for _, match := range mdFenceRegex.FindAllStringSubmatch(trimmed, -1) {
		candidate := strings.TrimSpace(match[1])
		candidate = strings.TrimSpace(structuredOutputLeadingTagRegex.ReplaceAllString(candidate, ""))
		if strings.HasPrefix(candidate, "{") && json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	for i, r := range trimmed {
		if r != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(trimmed[i:]))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
			continue
		}
		rest := strings.TrimSpace(trimmed[i+int(decoder.InputOffset()):])
		rest = strings.TrimSpace(structuredOutputLeadingTagRegex.ReplaceAllString(rest, ""))
		if rest == "" {
			return string(raw)
		}
	}
	return ""
}

func normalizeStructuredOutputText(content string) string {
	if extracted := extractStructuredJSONObject(content); extracted != "" {
		return extracted
	}
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		log.Printf("[bridge] structured output JSON-only normalization fallback (%d chars)", len(trimmed))
	}
	return trimmed
}

type preparedToolBridgeResponse struct {
	ToolCalls      []ToolCall
	Remaining      string
	DoneText       string
	WebSearchQuery string
	Protocol       string
	HasCalls       bool
	DroppedCalls   int
	InvalidDone    bool
}

func prepareToolBridgeResponse(content string, nativeToolUses []AgentValueEntry, allowedToolNames map[string]struct{}, aliasToOriginal map[string]string) preparedToolBridgeResponse {
	prepared := preparedToolBridgeResponse{Remaining: content, Protocol: "none"}

	if len(nativeToolUses) > 0 {
		prepared.Protocol = "native"
		nativeCalls := nativeToolUseToOpenAI(nativeToolUses)
		prepared.ToolCalls, prepared.DroppedCalls = filterDeclaredToolCalls(nativeCalls, allowedToolNames, aliasToOriginal)
		prepared.HasCalls = len(prepared.ToolCalls) > 0
	}
	if !prepared.HasCalls {
		parsedCalls, remaining, hasParsedCalls := parseToolCalls(content)
		if hasParsedCalls {
			prepared.Protocol = classifyTextToolBridgeProtocol(content)
			var dropped int
			prepared.ToolCalls, dropped = filterDeclaredToolCalls(parsedCalls, allowedToolNames, aliasToOriginal)
			prepared.DroppedCalls += dropped
			prepared.HasCalls = len(prepared.ToolCalls) > 0
			prepared.Remaining = remaining
		}
	}

	if prepared.HasCalls {
		var realCalls []ToolCall
		for _, tc := range prepared.ToolCalls {
			if tc.Function.Name == "__done__" {
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err == nil {
					if r, ok := args["result"].(string); ok && strings.TrimSpace(r) != "" {
						prepared.DoneText = r
					}
				}
				if prepared.DoneText == "" {
					prepared.InvalidDone = true
					log.Printf("[bridge] rejected __done__ with an empty result")
				} else {
					log.Printf("[bridge] __done__ intercepted: %s", prepared.DoneText)
				}
			} else {
				realCalls = append(realCalls, tc)
			}
		}
		prepared.ToolCalls = realCalls
		prepared.HasCalls = len(prepared.ToolCalls) > 0
		if prepared.DoneText != "" || prepared.InvalidDone {
			prepared.Protocol = "done"
		}
	}

	if prepared.HasCalls {
		var keptCalls []ToolCall
		for _, tc := range prepared.ToolCalls {
			if tc.Function.Name == "WebSearch" {
				var args map[string]interface{}
				var query string
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err == nil {
					if q, ok := args["query"].(string); ok {
						query = q
					}
				}
				if query == "" {
					query = tc.Function.Arguments
				}
				if prepared.WebSearchQuery != "" {
					prepared.WebSearchQuery = prepared.WebSearchQuery + "\n" + query
				} else {
					prepared.WebSearchQuery = query
				}
			} else {
				keptCalls = append(keptCalls, tc)
			}
		}
		prepared.ToolCalls = keptCalls
		prepared.HasCalls = len(prepared.ToolCalls) > 0
	}

	return prepared
}

func classifyTextToolBridgeProtocol(content string) string {
	if strings.HasPrefix(strings.TrimSpace(content), "<|") {
		return "sentinel_json"
	}
	return "text_json"
}

func filterDeclaredToolCalls(calls []ToolCall, allowedToolNames map[string]struct{}, aliasToOriginal map[string]string) ([]ToolCall, int) {
	filtered := make([]ToolCall, 0, len(calls))
	dropped := 0
	for _, tc := range calls {
		name := tc.Function.Name
		if original, ok := aliasToOriginal[name]; ok {
			name = original
			tc.Function.Name = original
		}
		_, allowed := allowedToolNames[name]
		if name == "done" && !allowed {
			name = "__done__"
			tc.Function.Name = name
		}
		if allowed || name == "__done__" {
			filtered = append(filtered, tc)
			continue
		}
		dropped++
		log.Printf("[bridge] filtered undeclared tool call %q", name)
	}
	return filtered, dropped
}

func declaredToolNames(tools []AnthropicTool) map[string]struct{} {
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool.Name != "" {
			names[tool.Name] = struct{}{}
		}
	}
	return names
}

func allowedToolNamesForChoice(tools []AnthropicTool, toolChoiceMode string) (map[string]struct{}, error) {
	names := declaredToolNames(tools)
	if !strings.HasPrefix(toolChoiceMode, "force:") {
		return names, nil
	}
	forcedName := strings.TrimSpace(strings.TrimPrefix(toolChoiceMode, "force:"))
	if _, ok := names[forcedName]; !ok {
		return nil, fmt.Errorf("tool_choice references undeclared tool %q", forcedName)
	}
	return map[string]struct{}{forcedName: {}}, nil
}

func applyStructuredOutputBridge(messages []ChatMessage, outputConfig *AnthropicOutputConfig) []ChatMessage {
	if outputConfig == nil || outputConfig.Format == nil || outputConfig.Format.Type != "json_schema" || outputConfig.Format.Schema == nil {
		return messages
	}

	schemaJSON, err := json.MarshalIndent(outputConfig.Format.Schema, "", "  ")
	if err != nil {
		schemaJSON, _ = json.Marshal(outputConfig.Format.Schema)
	}

	var prompt strings.Builder
	prompt.WriteString("\n\nReturn exactly one JSON object that matches this schema.\n")
	prompt.WriteString("Do not output markdown fences, explanations, or extra text.\n\n")
	prompt.WriteString("Schema:\n")
	prompt.Write(schemaJSON)
	prompt.WriteString("\n\nJSON only.")

	result := cloneChatMessages(messages)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role == "user" && result[i].ToolCallID == "" {
			result[i].Content += prompt.String()
			log.Printf("[bridge] structured output constraint appended without collapsing history (%d chars)", prompt.Len())
			return result
		}
	}
	result = append(result, ChatMessage{Role: "user", Content: strings.TrimSpace(prompt.String())})
	return result
}

// SSE events are constructed using maps for precise JSON field control
// (avoiding Go's omitempty dropping required empty-string fields)

// ========== Handler ==========

// HandleAnthropicMessages returns an HTTP handler for the /v1/messages endpoint (Anthropic Messages API)
func HandleAnthropicMessages(pool *AccountPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := "msg_" + generateUUIDv4()
		dashboardSettingsMu.RLock()
		defer dashboardSettingsMu.RUnlock()
		var activeSessionLock *Session
		var activeAccountLease *AccountLease
		releaseActiveAccountLease := func() {
			if activeAccountLease != nil {
				activeAccountLease.Release()
				activeAccountLease = nil
			}
		}
		defer func() {
			recovered := recover()
			releaseActiveAccountLease()
			if activeSessionLock != nil {
				// A panic or forgotten return path must not strand the
				// conversation mutex or leave a partially-mutated thread
				// available to the next request.
				globalSessionManager.DeleteIf(activeSessionLock.managerKey, activeSessionLock)
				activeSessionLock.unlockForRequest()
				activeSessionLock = nil
			}
			if recovered != nil {
				panic(recovered)
			}
		}()

		if r.Method != http.MethodPost {
			writeAnthropicError(w, requestID, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxAnthropicRequestBodyBytes)
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeAnthropicError(w, requestID, http.StatusRequestEntityTooLarge, "request body exceeds 64 MiB", "invalid_request_error")
				return
			}
			writeAnthropicError(w, requestID, http.StatusBadRequest, "failed to read request body: "+err.Error(), "invalid_request_error")
			return
		}
		if len(bodyBytes) == 0 {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "request body is required", "invalid_request_error")
			return
		}
		if json.Valid(bodyBytes) {
			LogAPIInputJSONBytes(requestID, "incoming /v1/messages request", bodyBytes)
		} else {
			LogAPIInputText(requestID, "incoming /v1/messages request (raw)", string(bodyBytes))
		}

		var req AnthropicRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error")
			return
		}

		if len(req.Messages) == 0 {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "messages is required", "invalid_request_error")
			return
		}

		clientToolChoice := req.ToolChoice
		req.ToolChoice = AppConfig.EffectiveToolChoice(clientToolChoice)
		if AppConfig.ToolChoicePolicy() != ToolChoicePolicyClient {
			log.Printf("[tool-choice] global policy %q overrides client mode %q",
				AppConfig.ToolChoicePolicy(), resolveToolChoiceMode(clientToolChoice))
		}

		model := req.Model
		usedDefaultModel := model == ""
		if model == "" {
			model = AppConfig.Proxy.DefaultModel
		}
		requestDiagnostic := RequestDiagnosticFromContext(r.Context())
		if requestDiagnostic != nil {
			requestDiagnostic.SetRequestedModel(model, usedDefaultModel)
			requestDiagnostic.SetClientRequest(req.Stream, req.ToolChoice, nil, len(req.Tools))
		}

		// ── ASK mode resolution ──
		// 1. Per-request override via "-ask" suffix on the model name
		//    (e.g. "claude-sonnet-4.6-ask"). Stripped before logging so
		//    downstream model-resolution sees the canonical name.
		// 2. Global default from /admin/settings (ask_mode_default).
		// Either source flips Notion's workflow useReadOnlyMode flag,
		// matching the frontend's "Ask" toggle.
		stripped, askFromModel := StripAskModeSuffix(model)
		if askFromModel {
			log.Printf("[ask-mode] %q -> %q (per-request override)", model, stripped)
		}
		model = stripped
		useReadOnlyMode := askFromModel || AppConfig.AskModeDefault()

		isResearcher := IsResearcherModel(model)
		if !isResearcher {
			if _, ok := ResolveModel(model); !ok {
				writeAnthropicError(
					w,
					requestID,
					http.StatusNotFound,
					fmt.Sprintf("model %q is not available; use GET /v1/models and send an exact model id", model),
					"not_found_error",
				)
				return
			}
		}
		// ── Detailed request logging ──
		logAnthropicRequest(req, model)

		// Convert Anthropic messages to internal ChatMessage format
		messages, fileAttachments, err := convertAnthropicMessages(req.System, req.Messages)
		if err != nil {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "invalid attachment: "+err.Error(), "invalid_request_error")
			return
		}
		if err := validateToolProtocol(messages); err != nil {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "invalid tool history: "+err.Error(), "invalid_request_error")
			return
		}
		// Preserve the exact client-visible history for safe unsalted session
		// chaining. Translated tool schemas and structured-output constraints
		// below are request-local implementation details and must not enter the key.
		sessionIdentityMessages := cloneChatMessages(messages)
		if len(fileAttachments) > 0 {
			log.Printf("[upload-debug] extracted %d file attachment(s) from request", len(fileAttachments))
		}
		// Log converted internal messages
		logConvertedMessages(messages)

		// Detect researcher mode — skip tools and file uploads
		if isResearcher {
			if len(fileAttachments) > 0 {
				log.Printf("[researcher] ignoring %d file attachment(s) — not supported in researcher mode", len(fileAttachments))
				fileAttachments = nil
			}
			if len(req.Tools) > 0 {
				log.Printf("[researcher] ignoring %d tool(s) — not supported in researcher mode", len(req.Tools))
			}
		}

		// ── Resolve search settings: header override > config default ──
		// Web search: default from config, overridable via X-Web-Search header
		effectiveWebSearch := AppConfig.WebSearchEnabled()
		if hdr := r.Header.Get("X-Web-Search"); hdr != "" {
			effectiveWebSearch = strings.EqualFold(hdr, "true") || hdr == "1"
			log.Printf("[search] X-Web-Search header override: %v", effectiveWebSearch)
		}
		// Workspace search: default from config, overridable via X-Workspace-Search header
		var enableWorkspaceSearch *bool
		if hdr := r.Header.Get("X-Workspace-Search"); hdr != "" {
			b := strings.EqualFold(hdr, "true") || hdr == "1"
			enableWorkspaceSearch = &b
			log.Printf("[search] X-Workspace-Search header override: %v", b)
		}

		// Convert Anthropic tools only while the external tool compatibility
		// bridge is enabled. When disabled, client tool definitions are ignored
		// and the request continues as a normal chat request.
		toolChoiceMode := resolveToolChoiceMode(req.ToolChoice)
		hasTools := toolBridgeActive(isResearcher, len(req.Tools)) && toolChoiceMode != "none"
		allowedToolNames, err := allowedToolNamesForChoice(req.Tools, toolChoiceMode)
		if err != nil {
			writeAnthropicError(w, requestID, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		enableWebSearch := effectiveWebSearch
		for _, tool := range req.Tools {
			if tool.Name == "WebSearch" {
				enableWebSearch = true
				break
			}
		}

		// Bind repeated client history to the real Notion thread. Current
		// Notion persists server Agent replies only on that thread; rebuilding a
		// fresh thread every request loses assistant context.
		var fingerprint string
		var session *Session
		var sessionSalt string
		stableSessionReuse := false
		var clientContinuationKey string
		rawMessageCount := 0
		if !isResearcher {
			sessionSalt = extractConversationSalt(req.Metadata)
			rawMessageCount = countConversationMessages(messages)
			clientContinuationKey = extractAssistantContinuationKeyWithContext(
				sessionIdentityMessages,
				fileAttachments,
				lastAssistantAnthropicMessageIndex(req.Messages),
			)
			stableSessionReuse = strings.TrimSpace(sessionSalt) != ""
			if stableSessionReuse {
				fingerprint = computeStableSessionFingerprint(sessionSalt)
			} else {
				// Stateless OpenAI/Anthropic clients usually resend the prior
				// assistant reply but provide no conversation ID. Bind only by
				// an exact reply signature that this proxy previously emitted;
				// never guess from a shared opening prompt.
				fingerprint = clientContinuationKey
			}
			if fingerprint != "" {
				session = globalSessionManager.Get(fingerprint)
			}
		}
		if requestDiagnostic != nil {
			switch {
			case isResearcher:
				requestDiagnostic.SetContextMode("not_applicable")
			case session != nil:
				requestDiagnostic.SetContextMode("thread_continuation")
			default:
				requestDiagnostic.SetContextMode("new_thread_replay")
			}
		}
		invalidateSession := func(contextMode string) {
			if isResearcher || fingerprint == "" || session == nil {
				return
			}
			globalSessionManager.DeleteIf(fingerprint, session)
			session = nil
			if requestDiagnostic != nil && contextMode != "" {
				requestDiagnostic.SetContextMode(contextMode)
			}
		}

		// Convert Anthropic tools to internal Tool format (done once, immutable).
		var convertedTools []Tool
		var bridgeTools []Tool
		var originalToToolAlias map[string]string
		var toolAliasToOriginal map[string]string
		if hasTools {
			for _, t := range req.Tools {
				convertedTools = append(convertedTools, Tool{
					Type: "function",
					Function: ToolFunction{
						Name:        t.Name,
						Description: t.Description,
						Parameters:  t.InputSchema,
					},
				})
			}
			// WebSearch remains in the compatibility protocol so the proxy can
			// execute it server-side. Its declaration also enables Notion's
			// native search for the request and is already reflected in the
			// session contract computed above.
			var toolDetectedWebSearch bool
			convertedTools, toolDetectedWebSearch = filterNativeSearchTools(convertedTools)
			if toolDetectedWebSearch {
				enableWebSearch = true
				log.Printf("[bridge] WebSearch detected — enabling Notion native search and preserving history")
			}
			bridgeTools, originalToToolAlias, toolAliasToOriginal = aliasClientTools(convertedTools)
		} else if !isResearcher && req.OutputConfig != nil && req.OutputConfig.Format != nil && req.OutputConfig.Format.Type == "json_schema" {
			messages = applyStructuredOutputBridge(messages, req.OutputConfig)
			if DebugLoggingEnabled() {
				log.Printf("[debug] === After structured output bridge (%d messages) ===", len(messages))
				for i, m := range messages {
					preview := truncateForLog(m.Content, 300)
					log.Printf("[debug]   [%d] role=%s toolcalls=%d content_len=%d: %s", i, m.Role, len(m.ToolCalls), len(m.Content), preview)
				}
			}
		}

		// Snapshot the original (pre-bridge) messages so failover to a
		// fresh account can rebuild a self-contained prompt that carries the
		// full conversation history (the user's "spread the chat to a new
		// account" requirement).
		originalMessages := cloneChatMessages(messages)

		// Try accounts with automatic failover
		tried := make(map[*Account]bool)
		selectionLimit := pool.Count()
		maxAccountCalls := inferenceAccountCallLimit(selectionLimit)
		accountCalls := 0
		var lastNonQuotaErr error
		emptyResponseCount := 0
		allNotionLoginsBusy := false
		liveCheckInterval := AppConfig.QuotaLiveCheckInterval()

		for selection := 0; selection < selectionLimit && accountCalls < maxAccountCalls; selection++ {
			var acc *Account
			var lease *AccountLease
			var leaseErr error

			if !isResearcher && selection == 0 && session != nil {
				var sessionAccount *Account
				if session.AccountID != "" {
					sessionAccount = pool.FindUsableByAccountID(session.AccountID)
				} else {
					legacyAccount, lookupErr := pool.FindByEmail(session.AccountEmail)
					if lookupErr == nil && !pool.isUnusable(legacyAccount) {
						sessionAccount = legacyAccount
					}
				}
				if sessionAccount == nil {
					log.Printf("[context] stored Notion thread account is unavailable; starting a fresh thread replay")
					invalidateSession("new_thread_account_switch")
				} else {
					lease, leaseErr = pool.LeaseAccount(sessionAccount)
					if errors.Is(leaseErr, ErrAllNotionLoginsBusy) {
						// The real Notion thread belongs to this login. A temporary
						// concurrency limit must not silently migrate the conversation
						// and turn an ordinary retry into a full-history replay.
						log.Printf("[concurrency] stored Notion thread login is busy; preserving the thread for client retry")
						allNotionLoginsBusy = true
						break
					} else if leaseErr == nil && lease != nil {
						acc = lease.Account()
					}
				}
			}
			if acc == nil {
				// Ordinary, research, and failover routing use the same
				// evidence-backed plan preference while atomically reserving a
				// slot on the selected Notion login.
				lease, leaseErr = pool.NextBestLease(tried)
				if errors.Is(leaseErr, ErrAllNotionLoginsBusy) {
					allNotionLoginsBusy = true
					break
				}
				if leaseErr != nil {
					lastNonQuotaErr = leaseErr
					break
				}
				if lease != nil {
					acc = lease.Account()
				}
			}
			if acc == nil {
				break
			}
			activeAccountLease = lease

			// Live quota pre-check: ensure the cached state is fresh enough
			// that we don't waste an inference call on an exhausted account.
			// Researcher mode has its own picker that already inspects quota.
			if !isResearcher && !pool.RefreshAccountQuota(acc, liveCheckInterval) {
				log.Printf("[quota-live] %s skipped (exhausted on live check)", acc.UserEmail)
				releaseActiveAccountLease()
				tried[acc] = true
				pool.MarkQuotaExhausted(acc)
				if session != nil &&
					((session.AccountID != "" && session.AccountID == acc.AccountID) ||
						(session.AccountID == "" && session.AccountEmail == acc.UserEmail)) {
					invalidateSession("new_thread_account_switch")
				}
				continue
			}
			tried[acc] = true

			// Build the request payload for this attempt. We always start
			// from the pristine `originalMessages` snapshot so account failover
			// and thread migration never copy gateway-generated route wrappers
			// into the client's history.
			attemptMessages := cloneChatMessages(originalMessages)
			if hasTools {
				attemptMessages = aliasToolNamesInMessages(attemptMessages, originalToToolAlias)
			}

			requestMessages := attemptMessages
			currentSession := session
			wasContinuation := false
			cacheSession := false
			bridgeFingerprint := ""
			bridgeContract := ""
			if !isResearcher {
				if fingerprint != "" {
					currentSession, wasContinuation, cacheSession = lockConversationSessionForRequest(
						globalSessionManager,
						fingerprint,
						session,
						rawMessageCount,
						acc.UserEmail,
						clientContinuationKey,
						acc.AccountID,
					)
					if cacheSession {
						session = currentSession
					} else {
						session = nil
					}
				} else {
					currentSession = newConversationSessionForAccount(acc.UserEmail, acc.AccountID)
					currentSession.lockForRequest()
				}
				activeSessionLock = currentSession
				// Publish the exact client-visible history chain before the
				// terminal HTTP/SSE event is sent. The session remains locked
				// until the outer attempt finishes, so a fast next turn cannot
				// observe a half-completed thread.
				oldFingerprint := fingerprint
				publishOldFingerprint := oldFingerprint
				if !cacheSession {
					// An ambiguous/duplicate request runs in an isolated thread
					// that is intentionally not bound to the old key. Its new,
					// unique assistant anchor can still be published safely.
					publishOldFingerprint = ""
				}
				currentSession.publishAssistant = func(message ChatMessage) {
					chain := append(cloneChatMessages(sessionIdentityMessages), message)
					nextFingerprint := computeConversationContinuationKeyWithContext(
						chain,
						fileAttachments,
					)
					if nextFingerprint == "" {
						return
					}
					currentSession.expectedClientKey = nextFingerprint
					if stableSessionReuse {
						return
					}
					if globalSessionManager.PublishReplacement(publishOldFingerprint, nextFingerprint, currentSession) {
						fingerprint = nextFingerprint
						session = currentSession
						cacheSession = true
					} else {
						session = nil
						cacheSession = false
					}
				}
			}

			// The full tool contract is a session-level transcript entry. It is
			// sent only for a new thread or when the client tool schemas change;
			// ordinary turns keep the raw user message untouched.
			if currentSession != nil && hasTools {
				bridgeFingerprint = toolBridgeFingerprint(bridgeTools)
				if currentSession.TurnCount == 0 || currentSession.ToolBridgeFingerprint != bridgeFingerprint {
					bridgeContract = buildToolBridgeContract(bridgeTools, currentSession.ToolBridgeFingerprint != "")
				}
			}
			if hasTools {
				if directive := toolBridgeDirective(toolChoiceMode, originalToToolAlias); directive != "" {
					requestMessages = addToolBridgeDirective(requestMessages, directive)
				}
			} else if currentSession != nil && currentSession.ToolBridgeFingerprint != "" {
				// A previous turn may have installed a tool contract while this
				// request explicitly has no tools. Make that per-request override
				// explicit so an old contract cannot cause a tool-shaped answer.
				requestMessages = addToolBridgeDirective(requestMessages,
					"Tool mode for this request: none. Do not call any tool; answer naturally.")
			}
			if DebugLoggingEnabled() && accountCalls == 0 {
				log.Printf("[debug] === Prepared request (%d messages, bridge_contract=%v) ===", len(requestMessages), bridgeContract != "")
				for i, m := range requestMessages {
					preview := truncateForLog(m.Content, 300)
					log.Printf("[debug]   [%d] role=%s toolcalls=%d content_len=%d: %s",
						i, m.Role, len(m.ToolCalls), len(m.Content), preview)
				}
			}

			accountCalls++
			log.Printf("[req] %s model=%s messages=%d stream=%v tools=%d attachments=%d account=%s continuation=%v (attempt %d/%d) [anthropic]",
				requestID, model, len(req.Messages), req.Stream, len(req.Tools), len(fileAttachments), acc.UserEmail, wasContinuation, accountCalls, maxAccountCalls)
			if requestDiagnostic != nil {
				requestDiagnostic.BeginAttempt(acc.UserEmail)
			}

			// Upload file attachments to Notion (if any) — skip for researcher mode
			var uploadedAttachments []UploadedAttachment
			attemptFileAttachments := fileAttachments
			if wasContinuation {
				attemptFileAttachments = attachmentsAfterMessageIndex(
					fileAttachments,
					lastAssistantAnthropicMessageIndex(req.Messages),
				)
			}
			if !isResearcher && len(attemptFileAttachments) > 0 {
				for i, fa := range attemptFileAttachments {
					log.Printf("[upload-debug] %s: uploading attachment %d/%d: %s (%s, %d bytes)",
						requestID, i+1, len(attemptFileAttachments), fa.FileName, fa.ContentType, len(fa.Data))
					uploadThreadID := ""
					createUploadThread := true
					if currentSession != nil {
						uploadThreadID = currentSession.ThreadID
						createUploadThread = currentSession.TurnCount == 0 && i == 0
					}
					uploaded, err := attachmentUploader(acc, &fa, uploadThreadID, createUploadThread)
					if err != nil {
						log.Printf("[upload] %s: attachment %d upload failed: %v", requestID, i+1, err)
						if currentSession != nil {
							if cacheSession {
								// A pre-published session must be invalidated before
								// releasing its lock. Otherwise a waiting next turn
								// could reuse a thread whose attachment setup failed.
								globalSessionManager.DeleteIf(fingerprint, currentSession)
								session = nil
							}
							currentSession.unlockForRequest()
							activeSessionLock = nil
						}
						if requestDiagnostic != nil {
							requestDiagnostic.FinishAttempt("upload_error", err)
						}
						releaseActiveAccountLease()
						writeAnthropicError(w, requestID, http.StatusBadGateway, "file upload failed: "+err.Error(), "api_error")
						return
					}
					uploadedAttachments = append(uploadedAttachments, *uploaded)
					log.Printf("[upload-debug] %s: attachment %d uploaded: %s", requestID, i+1, uploaded.AttachmentURL)
				}
			}

			// For streaming responses, default to emitting thinking blocks even when
			// the client did not explicitly request Anthropic thinking.
			hasThinking := req.Thinking != nil || req.Stream
			var reqErr error
			if isResearcher {
				// Researcher mode — always use thinking blocks for research progress
				hasThinking = true
				if req.Stream {
					reqErr = handleResearcherStream(r.Context(), w, acc, requestMessages, model, requestID, hasThinking, requestDiagnostic)
				} else {
					reqErr = handleResearcherNonStream(r.Context(), w, acc, requestMessages, model, requestID, hasThinking, requestDiagnostic)
				}
			} else if req.Stream {
				reqErr = handleAnthropicStream(r.Context(), w, acc, requestMessages, model, requestID, hasTools, len(bridgeTools) > 0, allowedToolNames, toolAliasToOriginal, toolChoiceMode, hasThinking, enableWebSearch, enableWorkspaceSearch, useReadOnlyMode, uploadedAttachments, req.OutputConfig, currentSession, requestDiagnostic, bridgeContract)
			} else {
				reqErr = handleAnthropicNonStream(r.Context(), w, acc, requestMessages, model, requestID, hasTools, len(bridgeTools) > 0, allowedToolNames, toolAliasToOriginal, toolChoiceMode, hasThinking, enableWebSearch, enableWorkspaceSearch, useReadOnlyMode, uploadedAttachments, req.OutputConfig, currentSession, requestDiagnostic, bridgeContract)
			}
			if currentSession != nil {
				if reqErr == nil {
					if hasTools && bridgeFingerprint != "" {
						currentSession.ToolBridgeFingerprint = bridgeFingerprint
					}
					completeConversationSessionLocked(currentSession, rawMessageCount, model)
					if cacheSession {
						globalSessionManager.Set(fingerprint, currentSession)
					}
				} else if cacheSession {
					// Invalidate while still holding the session lock. A waiter
					// cannot wake up and continue a thread that may contain a
					// failed/partial Agent step.
					globalSessionManager.DeleteIf(fingerprint, currentSession)
					session = nil
				}
				currentSession.unlockForRequest()
				activeSessionLock = nil
			}
			if requestDiagnostic != nil {
				requestDiagnostic.FinishAttempt(requestAttemptOutcome(reqErr), reqErr)
				if reqErr == nil && !hasTools {
					requestDiagnostic.SetToolBridge("none")
					requestDiagnostic.SetFinishReason("stop")
				}
			}
			// The upstream request and any session mutation are complete. Free
			// the login slot before quota diagnostics or failover selection.
			releaseActiveAccountLease()

			// Successful calls refresh quota diagnostics for the next request.
			// Error events are classified below; refreshing blindly here can
			// turn a model-specific failure into an account-wide false disable.
			if reqErr == nil {
				pool.RefreshAccountQuotaAsync(acc)
			}

			if reqErr != nil && errors.Is(reqErr, ErrStreamResponseStarted) {
				// A partial SSE response and an explicit error event already
				// reached the client. Retrying would splice two model responses
				// into one stream, and an HTTP error can no longer be written.
				return
			}

			if reqErr != nil && errors.Is(reqErr, ErrResearchQuotaExhausted) {
				// Research mode quota exhausted — account can still serve normal chat
				quota := acc.quotaInfoSnapshot()
				log.Printf("[research-quota] %s research quota exhausted (research_usage=%d), trying next account",
					acc.UserEmail, func() int {
						if quota != nil {
							return quota.ResearchModeUsage
						}
						return -1
					}())
				if requestDiagnostic != nil {
					requestDiagnostic.SetContextMode("full_replay_account_switch")
				}
				continue
			}
			if reqErr != nil && errors.Is(reqErr, ErrPromptTooLong) {
				lastNonQuotaErr = reqErr
				break
			}

			if reqErr != nil && errors.Is(reqErr, ErrEmptyResponse) {
				// An empty stream can be request-specific (notably context overflow).
				// The tried set is enough to rotate for this request; do not quarantine
				// an otherwise healthy account for later requests.
				log.Printf("[empty] %s returned empty response, trying next account", acc.UserEmail)
				emptyResponseCount++
				if requestDiagnostic != nil {
					requestDiagnostic.SetContextMode("full_replay_after_error")
				}
				invalidateSession("new_thread_after_error")
				if emptyResponseCount >= maxEmptyResponseAttempts {
					break
				}
				continue
			}

			if reqErr != nil && errors.Is(reqErr, ErrPremiumFeatureUnavailable) {
				// This event can be model/feature-specific. Only the V1
				// eligibility endpoint may classify a complimentary trial as
				// exhausted; never freeze an otherwise healthy account solely
				// from this inference event.
				if isFreePlan(acc) {
					if pool.RefreshAccountQuota(acc, 0) {
						log.Printf("[premium] %s feature unavailable but V1 quota remains eligible; trying next account", acc.UserEmail)
					} else {
						log.Printf("[premium] %s feature unavailable and V1 quota confirms exhausted; trying next account", acc.UserEmail)
					}
				} else {
					log.Printf("[premium] %s feature unavailable on this request; trying next account without quarantining it", acc.UserEmail)
				}
				if requestDiagnostic != nil {
					requestDiagnostic.SetContextMode("full_replay_account_switch")
				}
				invalidateSession("new_thread_account_switch")
				continue
			}

			if reqErr != nil {
				lastNonQuotaErr = reqErr
				failure := classifyAccountAttemptError(reqErr)
				if failure.Retryable {
					if failure.Reason == "auth_error" {
						invalid := pool.RecordAuthFailure(acc, defaultAccountFailureCooldown)
						if invalid {
							log.Printf("[health] %s failed with auth_error and is now marked auth_invalid, trying next account: %v", acc.UserEmail, reqErr)
						} else {
							log.Printf("[health] %s failed with auth_error, trying next account: %v", acc.UserEmail, reqErr)
						}
						invalidateSession("new_thread_account_switch")
						continue
					}
					if shouldQuarantineAccountFailure(failure.Reason) {
						pool.MarkTemporarilyUnavailable(acc, failure.Reason, defaultAccountFailureCooldown)
					}
					log.Printf("[health] %s failed with %s, trying next account: %v", acc.UserEmail, failure.Reason, reqErr)
					if requestDiagnostic != nil {
						requestDiagnostic.SetContextMode("full_replay_after_error")
					}
					invalidateSession("new_thread_after_error")
					continue
				}
				break
			}

			if reqErr == nil {
				pool.ClearTemporaryUnavailable(acc)
				if currentSession != nil {
					if requestDiagnostic != nil {
						if wasContinuation {
							requestDiagnostic.SetContextMode("thread_continuation")
						} else {
							requestDiagnostic.SetContextMode("new_thread")
						}
					}
				}
			}

			return
		}

		if lastNonQuotaErr != nil {
			status, message, errorType := inferenceHTTPError(lastNonQuotaErr)
			writeAnthropicError(w, requestID, status, message, errorType)
			return
		}
		if emptyResponseCount > 0 {
			writeAnthropicError(w, requestID, http.StatusBadGateway,
				fmt.Sprintf("notion returned no content on %d account(s); the prompt may exceed the model context or upstream ended without a terminal event", emptyResponseCount), "api_error")
			return
		}
		if allNotionLoginsBusy {
			w.Header().Set("Retry-After", "1")
			writeAnthropicError(w, requestID, http.StatusTooManyRequests,
				"all available Notion logins are busy; retry shortly", "rate_limit_error")
			return
		}
		writeAnthropicError(w, requestID, http.StatusServiceUnavailable,
			"all accounts exhausted after retries", "overloaded_error")
	}
}

func inferenceHTTPError(err error) (int, string, string) {
	if errors.Is(err, ErrPromptTooLong) {
		return http.StatusBadRequest, "context length exceeded: " + err.Error(), "invalid_request_error"
	}
	return http.StatusBadGateway, "notion API error: " + err.Error(), "api_error"
}

func requestAttemptOutcome(err error) string {
	if err == nil {
		return "success"
	}
	switch {
	case errors.Is(err, ErrResearchQuotaExhausted):
		return "research_quota_exhausted"
	case errors.Is(err, ErrEmptyResponse):
		return "empty_response"
	case errors.Is(err, ErrPromptTooLong):
		return "context_too_long"
	case errors.Is(err, ErrToolBridgeNoTool):
		return "required_tool_missing"
	case errors.Is(err, ErrPremiumFeatureUnavailable):
		return "premium_unavailable"
	}
	failure := classifyAccountAttemptError(err)
	if failure.Reason != "" {
		return failure.Reason
	}
	return "error"
}

func toolBridgeActive(isResearcher bool, toolCount int) bool {
	return !isResearcher && toolCount > 0 && AppConfig.ToolBridgeEnabled()
}

// convertAnthropicMessages converts Anthropic system + messages to internal ChatMessage format.
// It also extracts file attachments (image/document content blocks) for Notion upload.
func convertAnthropicMessages(system interface{}, msgs []AnthropicMessage) ([]ChatMessage, []FileAttachment, error) {
	var attachments []FileAttachment
	var result []ChatMessage

	// Handle system prompt
	if system != nil {
		switch s := system.(type) {
		case string:
			if s != "" {
				result = append(result, ChatMessage{Role: "system", Content: s})
			}
		case []interface{}:
			// System can be array of content blocks
			var parts []string
			for _, block := range s {
				if m, ok := block.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			if len(parts) > 0 {
				result = append(result, ChatMessage{Role: "system", Content: strings.Join(parts, "\n")})
			}
		}
	}

	// Build tool_use_id → name map by scanning all messages first
	toolIDToName := map[string]string{}
	for _, msg := range msgs {
		if blocks, ok := msg.Content.([]interface{}); ok {
			for _, block := range blocks {
				if m, ok := block.(map[string]interface{}); ok {
					if t, _ := m["type"].(string); t == "tool_use" {
						id, _ := m["id"].(string)
						name, _ := m["name"].(string)
						if id != "" && name != "" {
							toolIDToName[id] = name
						}
					}
				}
			}
		}
	}

	for msgIndex, msg := range msgs {
		cm := ChatMessage{Role: msg.Role}

		switch content := msg.Content.(type) {
		case string:
			cm.Content = content
		case []interface{}:
			// Array of content blocks
			var textParts []string
			for _, block := range content {
				if m, ok := block.(map[string]interface{}); ok {
					blockType, _ := m["type"].(string)
					switch blockType {
					case "text":
						if text, ok := m["text"].(string); ok {
							textParts = append(textParts, text)
						}
					case "thinking", "redacted_thinking":
						// Silently drop — CC echoes back our synthetic thinking blocks;
						// we don't need them in internal format since chain collapse
						// rebuilds context from scratch each turn.
					case "tool_use":
						// Convert to proper ToolCall for multi-turn strategy detection
						name, _ := m["name"].(string)
						id, _ := m["id"].(string)
						inputRaw, _ := json.Marshal(m["input"])
						cm.ToolCalls = append(cm.ToolCalls, ToolCall{
							ID:   id,
							Type: "function",
							Function: ToolCallFunction{
								Name:      name,
								Arguments: string(inputRaw),
							},
						})
					case "image":
						// Extract image from base64 or URL source
						if source, ok := m["source"].(map[string]interface{}); ok {
							fa, err := extractFileFromSource(source, "image")
							if err != nil {
								return nil, nil, err
							}
							fa.MessageIndex = msgIndex
							attachments = append(attachments, *fa)
						}
					case "document":
						// Extract PDF/text document from base64, URL, or file source
						if source, ok := m["source"].(map[string]interface{}); ok {
							fa, err := extractFileFromSource(source, "document")
							if err != nil {
								return nil, nil, err
							}
							fa.MessageIndex = msgIndex
							attachments = append(attachments, *fa)
						}
					case "tool_result":
						// Convert tool_result to tool role message
						toolUseID, _ := m["tool_use_id"].(string)
						var resultParts []string
						if c, ok := m["content"].(string); ok {
							resultParts = append(resultParts, c)
						} else if c, ok := m["content"].([]interface{}); ok {
							for _, cb := range c {
								if cbm, ok := cb.(map[string]interface{}); ok {
									switch nestedType, _ := cbm["type"].(string); nestedType {
									case "text":
										if text, ok := cbm["text"].(string); ok {
											resultParts = append(resultParts, text)
										}
									case "image", "document":
										if source, ok := cbm["source"].(map[string]interface{}); ok {
											attachment, err := extractFileFromSource(source, nestedType)
											if err != nil {
												return nil, nil, err
											}
											attachment.MessageIndex = msgIndex
											attachments = append(attachments, *attachment)
											resultParts = append(resultParts, fmt.Sprintf("[%s attachment]", nestedType))
										}
									}
								}
							}
						}
						result = append(result, ChatMessage{
							Role:       "tool",
							Content:    strings.Join(resultParts, "\n"),
							ToolCallID: toolUseID,
							Name:       toolIDToName[toolUseID],
						})
						continue
					}
				}
			}
			if len(textParts) > 0 {
				cm.Content = strings.Join(textParts, "")
			}
		}

		if cm.Content != "" || cm.Role == "assistant" || len(cm.ToolCalls) > 0 {
			result = append(result, cm)
		}
	}

	return result, attachments, nil
}

func attachmentsAfterMessageIndex(attachments []FileAttachment, messageIndex int) []FileAttachment {
	if len(attachments) == 0 {
		return nil
	}
	filtered := make([]FileAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.MessageIndex > messageIndex {
			filtered = append(filtered, attachment)
		}
	}
	return filtered
}

const maxRemoteAttachmentBytes = 25 * 1024 * 1024

func isUnsafeAttachmentIP(ip net.IP) bool {
	return ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func safeAttachmentDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid attachment address: %w", err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment host: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("attachment host resolved to no addresses")
	}
	for _, resolved := range ips {
		if isUnsafeAttachmentIP(resolved.IP) {
			return nil, fmt.Errorf("attachment URL resolves to a private or reserved address")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

var remoteAttachmentHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           safeAttachmentDialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many attachment redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("unsupported attachment redirect scheme")
		}
		return nil
	},
}

// extractFileFromSource extracts file data from an Anthropic content block source.
// Supports base64 and URL source types. blockKind is "image" or "document".
func extractFileFromSource(source map[string]interface{}, blockKind string) (*FileAttachment, error) {
	srcType, _ := source["type"].(string)

	switch srcType {
	case "base64":
		mediaType, _ := source["media_type"].(string)
		data64, _ := source["data"].(string)
		if data64 == "" {
			return nil, fmt.Errorf("%s base64 source is empty", blockKind)
		}
		if base64.StdEncoding.DecodedLen(len(data64)) > maxRemoteAttachmentBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", blockKind, maxRemoteAttachmentBytes)
		}
		decoded, err := base64.StdEncoding.DecodeString(data64)
		if err != nil {
			return nil, fmt.Errorf("decode base64 %s: %w", blockKind, err)
		}
		if mediaType == "" {
			if blockKind == "image" {
				mediaType = "image/png"
			} else {
				mediaType = "application/pdf"
			}
		}
		ext := mimeToExt(mediaType)
		return &FileAttachment{
			Data:        decoded,
			FileName:    generateUUIDv4() + ext,
			ContentType: mediaType,
		}, nil

	case "url":
		urlStr, _ := source["url"].(string)
		if urlStr == "" {
			return nil, fmt.Errorf("%s URL source is empty", blockKind)
		}
		parsedURL, err := url.Parse(urlStr)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Hostname() == "" {
			return nil, fmt.Errorf("invalid %s URL", blockKind)
		}
		// Download only from public HTTP(S) addresses, with bounded time and
		// size. The custom dialer revalidates every redirect target and blocks
		// loopback, link-local, and private networks.
		resp, err := remoteAttachmentHTTPClient.Get(parsedURL.String())
		if err != nil {
			return nil, fmt.Errorf("download %s URL: %w", blockKind, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("%s URL returned HTTP %d", blockKind, resp.StatusCode)
		}
		if resp.ContentLength > maxRemoteAttachmentBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", blockKind, maxRemoteAttachmentBytes)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteAttachmentBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %s URL response: %w", blockKind, err)
		}
		if len(data) > maxRemoteAttachmentBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", blockKind, maxRemoteAttachmentBytes)
		}
		mediaType := resp.Header.Get("Content-Type")
		if mediaType == "" {
			if blockKind == "image" {
				mediaType = "image/png"
			} else {
				mediaType = "application/pdf"
			}
		}
		// Strip charset suffix if present (e.g. "image/png; charset=utf-8")
		if idx := strings.Index(mediaType, ";"); idx > 0 {
			mediaType = strings.TrimSpace(mediaType[:idx])
		}
		ext := mimeToExt(mediaType)
		return &FileAttachment{
			Data:        data,
			FileName:    generateUUIDv4() + ext,
			ContentType: mediaType,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported source type %q for %s block", srcType, blockKind)
	}
}

func streamAnthropicTextResponse(w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasThinking bool, disableBuiltin bool, outputConfig *AnthropicOutputConfig, callOpts CallOptions) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, requestID, http.StatusInternalServerError, "streaming not supported", "api_error")
		return nil
	}

	var fullContent strings.Builder
	var thinkingForLog strings.Builder
	var finalUsage *UsageInfo
	defer func() {
		recordCompletedAttemptUsage(acc, model, finalUsage, callOpts.RequestDiagnostic)
	}()
	var knownCitationURLs []string
	var knownCitationDocs []CitationCandidate
	knownToolCallURLs := make(map[string][]string)
	cr := newCitationReplacer(&knownCitationURLs, &knownCitationDocs, &knownToolCallURLs)
	headersSent := false
	thinkingBlockOpen := false
	textBlockOpen := false
	blockIndex := 0
	callOpts.KnownCitationURLs = &knownCitationURLs
	callOpts.KnownCitationDocs = &knownCitationDocs
	callOpts.KnownToolCallURLs = &knownToolCallURLs
	jsonOnlyOutput := isJSONSchemaOutput(outputConfig)

	ensureHeaders := func() {
		if headersSent {
			return
		}
		headersSent = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		inputTokens := 0
		if finalUsage != nil {
			inputTokens = finalUsage.PromptTokens
		}
		sendAnthropicSSE(w, flusher, "message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            requestID,
				"type":          "message",
				"role":          "assistant",
				"content":       []interface{}{},
				"model":         model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]interface{}{"input_tokens": inputTokens, "output_tokens": 0},
			},
		})
		sendAnthropicSSE(w, flusher, "ping", map[string]string{"type": "ping"})
	}

	closeThinkingBlock := func(signature string) {
		if !thinkingBlockOpen {
			return
		}
		if signature == "" {
			signature = generateFakeSignature()
		}
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]interface{}{"type": "signature_delta", "signature": signature},
		})
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": blockIndex,
		})
		blockIndex++
		thinkingBlockOpen = false
	}

	ensureTextBlock := func() {
		ensureHeaders()
		if !textBlockOpen {
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			textBlockOpen = true
		}
	}

	if hasThinking {
		callOpts.ThinkingCallback = func(delta string, done bool, signature string) {
			if done {
				closeThinkingBlock(signature)
				return
			}
			if delta == "" {
				return
			}
			thinkingForLog.WriteString(delta)
			ensureHeaders()
			if !thinkingBlockOpen {
				sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
					"type":          "content_block_start",
					"index":         blockIndex,
					"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
				})
				thinkingBlockOpen = true
			}
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": delta},
			})
		}
	}

	cbErr := CallInference(acc, messages, model, disableBuiltin, func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			fullContent.WriteString(delta)
			if !jsonOnlyOutput {
				cleaned := cr.Process(delta)
				if cleaned != "" {
					ensureTextBlock()
					sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": blockIndex,
						"delta": map[string]interface{}{"type": "text_delta", "text": cleaned},
					})
				}
			}
		}
		if usage != nil {
			finalUsage = usage
		}
	}, callOpts)

	if cbErr != nil {
		if !headersSent {
			log.Printf("[err] %s: %v", requestID, cbErr)
			return cbErr
		}
		return writeAnthropicStreamError(w, flusher, requestID, cbErr)
	}

	if strings.TrimSpace(fullContent.String()) == "" {
		log.Printf("[warn] %s: empty response from Notion, will retry", requestID)
		if headersSent {
			return writeAnthropicStreamError(w, flusher, requestID, ErrEmptyResponse)
		}
		return ErrEmptyResponse
	}

	if thinkingBlockOpen {
		closeThinkingBlock("")
	}

	if !jsonOnlyOutput {
		if flushed := cr.Flush(); flushed != "" {
			ensureTextBlock()
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]interface{}{"type": "text_delta", "text": flushed},
			})
		}
	} else {
		normalized := normalizeStructuredOutputText(fullContent.String())
		if normalized != "" {
			ensureTextBlock()
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]interface{}{"type": "text_delta", "text": normalized},
			})
		}
	}

	if textBlockOpen {
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": blockIndex,
		})
		blockIndex++
	}

	urls := cr.URLs()
	if !jsonOnlyOutput {
		if sourcesText := formatCitationSources(urls); sourcesText != "" {
			ensureHeaders()
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]interface{}{"type": "text_delta", "text": sourcesText},
			})
			sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
				"type": "content_block_stop", "index": blockIndex,
			})
			blockIndex++
		}
	}

	assistantText := renderAnthropicCitationText(fullContent.String(), knownCitationURLs, knownCitationDocs, knownToolCallURLs)
	if jsonOnlyOutput {
		assistantText = normalizeStructuredOutputText(fullContent.String())
	}
	callOpts.Session.publishAssistantContinuation(ChatMessage{
		Role:    "assistant",
		Content: assistantText,
	})

	outputTokens := 0
	inputTokens := 0
	if finalUsage != nil {
		inputTokens = reportedInputTokens(finalUsage.PromptTokens, callOpts.RequestDiagnostic)
		outputTokens = finalUsage.CompletionTokens
	}
	ensureHeaders()
	sendAnthropicSSE(w, flusher, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]interface{}{"input_tokens": inputTokens, "output_tokens": outputTokens},
	})
	sendAnthropicSSE(w, flusher, "message_stop", map[string]string{"type": "message_stop"})

	var contentBlocks []AnthropicContentBlock
	if hasThinking && thinkingForLog.Len() > 0 {
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type:      "thinking",
			Thinking:  thinkingForLog.String(),
			Signature: generateFakeSignature(),
		})
	}
	if fullContent.Len() > 0 {
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type: "text",
			Text: assistantText,
		})
	}
	LogAPIOutputJSON(requestID, "anthropic stream summary", AnthropicResponse{
		ID:         requestID,
		Type:       "message",
		Role:       "assistant",
		Content:    contentBlocks,
		Model:      model,
		StopReason: strPtr("end_turn"),
		Usage: &AnthropicUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	})

	log.Printf("[thinking] %s streamed text=%d chars sources=%d thinking=%v", requestID, fullContent.Len(), len(urls), hasThinking)
	return nil
}

const maxToolProtocolProbeBytes = 4 * 1024

var (
	jsonFirstKeyPrefixRegex = regexp.MustCompile(`(?s)^\{\s*"([^"\\]+)"\s*:`)
	jsonNamedSecondKeyRegex = regexp.MustCompile(`(?s)^\{\s*"name"\s*:\s*"[^"]*"\s*,\s*"([^"\\]+)"\s*:`)
)

func jsonToolProtocolPrefixState(content string) (protocol, undecided bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return false, false
	}
	firstKey := jsonFirstKeyPrefixRegex.FindStringSubmatch(trimmed)
	if len(firstKey) < 2 {
		if json.Valid([]byte(trimmed)) || len(trimmed) >= maxToolProtocolProbeBytes {
			return false, false
		}
		return false, true
	}
	switch firstKey[1] {
	case "tool_call":
		return true, false
	case "name":
		if secondKey := jsonNamedSecondKeyRegex.FindStringSubmatch(trimmed); len(secondKey) >= 2 {
			return secondKey[1] == "arguments", false
		}
		if json.Valid([]byte(trimmed)) {
			_, _, hasToolCall := parseToolCalls(trimmed)
			return hasToolCall, false
		}
		if len(trimmed) >= maxToolProtocolProbeBytes {
			return false, false
		}
		return false, true
	default:
		return false, false
	}
}

func toolProtocolPrefixState(content string) (protocol, undecided bool) {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	if trimmed == "" {
		return false, true
	}
	for _, prefix := range []string{"<|", "<tool_call>", "```tool_call"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true, false
		}
		if strings.HasPrefix(prefix, trimmed) {
			return false, true
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		return jsonToolProtocolPrefixState(trimmed)
	}
	if strings.HasPrefix(trimmed, "```") {
		afterFence := trimmed[3:]
		if afterFence == "" {
			return false, true
		}
		lowerAfterFence := strings.ToLower(afterFence)
		if strings.HasPrefix("json", lowerAfterFence) {
			return false, true
		}
		if strings.HasPrefix(lowerAfterFence, "json") {
			afterFence = afterFence[len("json"):]
			if afterFence == "" {
				return false, true
			}
			if !strings.ContainsRune(" \t\r\n", rune(afterFence[0])) {
				return false, false
			}
		}
		payload := strings.TrimLeft(afterFence, " \t\r\n")
		if payload == "" {
			return false, true
		}
		if strings.HasPrefix(payload, "{") {
			return jsonToolProtocolPrefixState(payload)
		}
		return false, false
	}
	return false, false
}

type incrementalToolStream struct {
	mode    string
	pending strings.Builder
}

func newIncrementalToolStream(toolChoiceMode string) *incrementalToolStream {
	mode := "undecided"
	if toolChoiceMode == "required" || strings.HasPrefix(toolChoiceMode, "force:") {
		mode = "protocol"
	}
	return &incrementalToolStream{mode: mode}
}

func (stream *incrementalToolStream) Push(delta string) string {
	if delta == "" {
		return ""
	}
	switch stream.mode {
	case "text":
		return delta
	case "protocol":
		return ""
	}
	stream.pending.WriteString(delta)
	protocol, undecided := toolProtocolPrefixState(stream.pending.String())
	if protocol {
		stream.mode = "protocol"
		stream.pending.Reset()
		return ""
	}
	if undecided {
		return ""
	}
	stream.mode = "text"
	text := stream.pending.String()
	stream.pending.Reset()
	return text
}

func (stream *incrementalToolStream) FlushText() string {
	if stream.mode != "undecided" {
		return ""
	}
	stream.mode = "text"
	text := stream.pending.String()
	stream.pending.Reset()
	return text
}

// handleAnthropicStream streams thinking and ordinary text as it arrives. Only
// a response that begins like a tool protocol is buffered until it can be
// validated and converted into tool_use events.
func handleAnthropicStream(ctx context.Context, w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasTools, hasBridgedClientTools bool, allowedToolNames map[string]struct{}, toolAliasToOriginal map[string]string, toolChoiceMode string, hasThinking bool, enableWebSearch bool, enableWorkspaceSearch *bool, useReadOnlyMode bool, attachments []UploadedAttachment, outputConfig *AnthropicOutputConfig, session *Session, requestDiagnostic *RequestDiagnostic, bridgeContracts ...string) error {
	bridgeContract := firstNonEmptyString(bridgeContracts)
	if !hasTools {
		callOpts := CallOptions{
			Context:               ctx,
			HasClientTools:        hasBridgedClientTools,
			EnableWebSearch:       enableWebSearch,
			EnableWorkspaceSearch: enableWorkspaceSearch,
			UseReadOnlyMode:       useReadOnlyMode,
			Attachments:           attachments,
			RequestID:             requestID,
			Session:               session,
			ToolBridgeContract:    bridgeContract,
			RequestDiagnostic:     requestDiagnostic,
		}
		return streamAnthropicTextResponse(w, acc, messages, model, requestID, hasThinking, AppConfig.Proxy.DisableNotionPrompt, outputConfig, callOpts)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, requestID, http.StatusInternalServerError, "streaming not supported", "api_error")
		return nil
	}

	var fullContent strings.Builder
	var thinkingForLog strings.Builder
	var finalUsage *UsageInfo
	var nativeToolUses []AgentValueEntry
	var thinkingBlocks []ThinkingBlock
	var knownCitationURLs []string
	var knownCitationDocs []CitationCandidate
	knownToolCallURLs := make(map[string][]string)
	defer func() { recordCompletedAttemptUsage(acc, model, finalUsage, requestDiagnostic) }()

	headersSent := false
	thinkingOpen := false
	textOpen := false
	blockIndex := 0
	var clientVisibleText strings.Builder
	toolStream := newIncrementalToolStream(toolChoiceMode)
	requiresToolCall := toolChoiceMode == "required" || strings.HasPrefix(toolChoiceMode, "force:")

	ensureHeaders := func() {
		if headersSent {
			return
		}
		headersSent = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		sendAnthropicSSE(w, flusher, "message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": requestID, "type": "message", "role": "assistant", "content": []interface{}{}, "model": model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
			},
		})
		sendAnthropicSSE(w, flusher, "ping", map[string]string{"type": "ping"})
	}
	closeThinking := func(signature string) {
		if !thinkingOpen {
			return
		}
		if signature == "" {
			signature = generateFakeSignature()
		}
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": blockIndex,
			"delta": map[string]interface{}{"type": "signature_delta", "signature": signature},
		})
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": blockIndex})
		blockIndex++
		thinkingOpen = false
	}
	ensureText := func() {
		ensureHeaders()
		if thinkingOpen {
			closeThinking("")
		}
		if !textOpen {
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type": "content_block_start", "index": blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			textOpen = true
		}
	}
	emitText := func(text string) {
		if text == "" {
			return
		}
		clientVisibleText.WriteString(text)
		ensureText()
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": blockIndex,
			"delta": map[string]interface{}{"type": "text_delta", "text": text},
		})
	}
	closeText := func() {
		if !textOpen {
			return
		}
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": blockIndex})
		blockIndex++
		textOpen = false
	}

	callOpts := CallOptions{
		Context:               ctx,
		NativeToolUses:        &nativeToolUses,
		ThinkingBlocks:        &thinkingBlocks,
		HasClientTools:        hasBridgedClientTools,
		EnableWebSearch:       enableWebSearch,
		EnableWorkspaceSearch: enableWorkspaceSearch,
		UseReadOnlyMode:       useReadOnlyMode,
		Attachments:           attachments,
		KnownCitationURLs:     &knownCitationURLs,
		KnownCitationDocs:     &knownCitationDocs,
		KnownToolCallURLs:     &knownToolCallURLs,
		RequestID:             requestID,
		Session:               session,
		ToolBridgeContract:    bridgeContract,
		RequestDiagnostic:     requestDiagnostic,
	}
	if hasThinking {
		callOpts.ThinkingCallback = func(delta string, done bool, signature string) {
			if delta != "" {
				thinkingForLog.WriteString(delta)
			}
			// Keep required/forced responses retryable until a valid tool call has
			// been parsed. Auto mode continues to stream thinking immediately.
			if requiresToolCall {
				return
			}
			if done {
				closeThinking(signature)
				return
			}
			if delta == "" {
				return
			}
			ensureHeaders()
			if !thinkingOpen {
				sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
					"type": "content_block_start", "index": blockIndex,
					"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
				})
				thinkingOpen = true
			}
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": blockIndex,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": delta},
			})
		}
	}

	cbErr := CallInference(acc, messages, model, AppConfig.Proxy.DisableNotionPrompt, func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			fullContent.WriteString(delta)
			emitText(toolStream.Push(delta))
		}
		if usage != nil {
			finalUsage = usage
		}
	}, callOpts)
	if cbErr != nil && !headersSent {
		return cbErr
	}
	if cbErr != nil {
		return writeAnthropicStreamError(w, flusher, requestID, cbErr)
	}

	content := fullContent.String()
	if strings.TrimSpace(content) == "" && len(nativeToolUses) == 0 {
		if headersSent {
			return writeAnthropicStreamError(w, flusher, requestID, ErrEmptyResponse)
		}
		return ErrEmptyResponse
	}
	emitText(toolStream.FlushText())
	streamMode := toolStream.mode
	if thinkingOpen {
		closeThinking("")
	}

	prepared := prepareToolBridgeResponse(content, nativeToolUses, allowedToolNames, toolAliasToOriginal)
	if requestDiagnostic != nil {
		requestDiagnostic.SetToolBridge(prepared.Protocol)
	}
	actionDetected := prepared.HasCalls || prepared.WebSearchQuery != "" || prepared.DoneText != ""
	if requiresToolCall && !prepared.HasCalls && prepared.WebSearchQuery == "" {
		return ErrToolBridgeNoTool
	}
	if !actionDetected && streamMode == "protocol" {
		emitText(prepared.Remaining)
	}
	if streamMode == "protocol" && prepared.DoneText != "" {
		emitText(prepared.DoneText)
	}
	closeText()

	if requiresToolCall && thinkingForLog.Len() > 0 {
		ensureHeaders()
		sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": blockIndex,
			"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
		})
		thinkingOpen = true
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": blockIndex,
			"delta": map[string]interface{}{"type": "thinking_delta", "thinking": thinkingForLog.String()},
		})
		signature := ""
		if len(thinkingBlocks) > 0 {
			signature = thinkingBlocks[len(thinkingBlocks)-1].Signature
		}
		closeThinking(signature)
	}

	if prepared.WebSearchQuery != "" {
		ensureHeaders()
		searchUsage, searchText, searchErr := streamWebSearch(ctx, w, flusher, acc, prepared.WebSearchQuery, model, requestID, &blockIndex, hasThinking, session)
		if searchErr != nil {
			return writeAnthropicStreamError(w, flusher, requestID, searchErr)
		}
		finalUsage = mergeSequentialUsage(finalUsage, searchUsage)
		clientVisibleText.WriteString(searchText)
	}

	for _, call := range prepared.ToolCalls {
		ensureHeaders()
		sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": blockIndex,
			"content_block": map[string]interface{}{
				"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": map[string]interface{}{},
			},
		})
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": blockIndex,
			"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": call.Function.Arguments},
		})
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": blockIndex})
		blockIndex++
	}

	continuation := ChatMessage{
		Role:    "assistant",
		Content: clientVisibleText.String(),
	}
	continuation.ToolCalls = append(continuation.ToolCalls, prepared.ToolCalls...)
	session.publishAssistantContinuation(continuation)

	stopReason := "end_turn"
	if prepared.HasCalls {
		stopReason = "tool_use"
	}
	inputTokens, outputTokens := 0, 0
	if finalUsage != nil {
		inputTokens = reportedInputTokens(finalUsage.PromptTokens, requestDiagnostic)
		outputTokens = finalUsage.CompletionTokens
	}
	ensureHeaders()
	if requestDiagnostic != nil {
		if prepared.HasCalls {
			requestDiagnostic.SetFinishReason("tool_calls")
		} else {
			requestDiagnostic.SetFinishReason("stop")
		}
	}
	sendAnthropicSSE(w, flusher, "message_delta", map[string]interface{}{
		"type": "message_delta", "delta": map[string]interface{}{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]interface{}{"input_tokens": inputTokens, "output_tokens": outputTokens},
	})
	sendAnthropicSSE(w, flusher, "message_stop", map[string]string{"type": "message_stop"})
	log.Printf("[bridge] %s streamed mode=%s text=%d tool_calls=%d", requestID, streamMode, len(content), len(prepared.ToolCalls))
	return nil
}

// handleAnthropicNonStream handles non-streaming Anthropic response
func handleAnthropicNonStream(ctx context.Context, w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasTools, hasBridgedClientTools bool, allowedToolNames map[string]struct{}, toolAliasToOriginal map[string]string, toolChoiceMode string, hasThinking bool, enableWebSearch bool, enableWorkspaceSearch *bool, useReadOnlyMode bool, attachments []UploadedAttachment, outputConfig *AnthropicOutputConfig, session *Session, requestDiagnostic *RequestDiagnostic, bridgeContracts ...string) error {
	bridgeContract := firstNonEmptyString(bridgeContracts)
	var fullContent strings.Builder
	var finalUsage *UsageInfo
	defer func() {
		recordCompletedAttemptUsage(acc, model, finalUsage, requestDiagnostic)
	}()
	var nativeToolUses []AgentValueEntry
	var thinkingBlocks []ThinkingBlock
	var thinkingProcess strings.Builder
	var thinkingSignature string
	var knownCitationURLs []string
	var knownCitationDocs []CitationCandidate
	knownToolCallURLs := make(map[string][]string)

	callOpts := CallOptions{
		Context:               ctx,
		ThinkingBlocks:        &thinkingBlocks,
		HasClientTools:        hasBridgedClientTools,
		EnableWebSearch:       enableWebSearch,
		EnableWorkspaceSearch: enableWorkspaceSearch,
		UseReadOnlyMode:       useReadOnlyMode,
		Attachments:           attachments,
		KnownCitationURLs:     &knownCitationURLs,
		KnownCitationDocs:     &knownCitationDocs,
		KnownToolCallURLs:     &knownToolCallURLs,
		RequestID:             requestID,
		Session:               session,
		ToolBridgeContract:    bridgeContract,
		RequestDiagnostic:     requestDiagnostic,
	}
	if hasThinking {
		callOpts.ThinkingCallback = func(delta string, done bool, signature string) {
			if delta != "" {
				thinkingProcess.WriteString(delta)
			}
			if done && signature != "" {
				thinkingSignature = signature
			}
		}
	}

	var err error
	if hasTools {
		callOpts.NativeToolUses = &nativeToolUses
		// Client tools are represented by the session-level bridge contract.
		err = CallInference(acc, messages, model, AppConfig.Proxy.DisableNotionPrompt, func(delta string, done bool, usage *UsageInfo) {
			if delta != "" {
				fullContent.WriteString(delta)
			}
			if usage != nil {
				finalUsage = usage
			}
		}, callOpts)
	} else {
		err = CallInference(acc, messages, model, AppConfig.Proxy.DisableNotionPrompt, func(delta string, done bool, usage *UsageInfo) {
			if delta != "" {
				fullContent.WriteString(delta)
			}
			if usage != nil {
				finalUsage = usage
			}
		}, callOpts)
	}

	if err != nil {
		log.Printf("[err] %s: %v", requestID, err)
		return err
	}

	content := fullContent.String()

	// Empty response: Notion returned 200 but produced no text — retry on next account
	if strings.TrimSpace(content) == "" && len(nativeToolUses) == 0 {
		log.Printf("[warn] %s: empty non-stream response from Notion, will retry", requestID)
		return ErrEmptyResponse
	}

	var prepared preparedToolBridgeResponse
	if hasTools {
		prepared = prepareToolBridgeResponse(content, nativeToolUses, allowedToolNames, toolAliasToOriginal)
		if requestDiagnostic != nil {
			requestDiagnostic.SetToolBridge(prepared.Protocol)
		}
		requiresCall := toolChoiceMode == "required" || strings.HasPrefix(toolChoiceMode, "force:")
		if requiresCall && !prepared.HasCalls && prepared.WebSearchQuery == "" {
			log.Printf("[bridge] %s required a client tool call but received plain text", requestID)
			return ErrToolBridgeNoTool
		}
	}

	aUsage := &AnthropicUsage{}
	if finalUsage != nil {
		aUsage.InputTokens = reportedInputTokens(finalUsage.PromptTokens, requestDiagnostic)
		aUsage.OutputTokens = finalUsage.CompletionTokens
	}

	var contentBlocks []AnthropicContentBlock
	stopReason := "end_turn"

	// Prepend process-oriented thinking when available, otherwise fall back to raw blocks.
	if hasThinking && thinkingProcess.Len() > 0 {
		sig := thinkingSignature
		if sig == "" {
			sig = generateFakeSignature()
		}
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type:      "thinking",
			Thinking:  thinkingProcess.String(),
			Signature: sig,
		})
	} else if hasThinking && len(thinkingBlocks) > 0 {
		for _, tb := range thinkingBlocks {
			sig := tb.Signature
			if sig == "" {
				sig = generateFakeSignature()
			}
			contentBlocks = append(contentBlocks, AnthropicContentBlock{
				Type:      "thinking",
				Thinking:  tb.Content,
				Signature: sig,
			})
		}
	}

	if hasTools {
		toolCalls := prepared.ToolCalls
		remaining := prepared.Remaining
		hasCalls := prepared.HasCalls
		doneText := prepared.DoneText

		// Intercept WebSearch tool calls → execute via Notion's native search
		if prepared.WebSearchQuery != "" {
			log.Printf("[bridge] WebSearch intercepted — executing via Notion native search: %q", prepared.WebSearchQuery)
			searchResult, searchUsage, searchErr := executeWebSearch(ctx, acc, prepared.WebSearchQuery, model, requestID, session)
			if searchErr == nil && searchResult != "" {
				if doneText != "" {
					doneText = doneText + "\n\n" + searchResult
				} else {
					doneText = searchResult
				}
				finalUsage = mergeSequentialUsage(finalUsage, searchUsage)
			} else if searchErr != nil {
				return searchErr
			}
		}

		// When tool actions were detected, suppress residual framing / identity text.
		if remaining != "" && hasCalls {
			log.Printf("[bridge] suppressed %d chars of residual tool framing text", len(remaining))
		} else if remaining != "" {
			contentBlocks = append(contentBlocks, AnthropicContentBlock{Type: "text", Text: remaining})
		}
		if doneText != "" {
			contentBlocks = append(contentBlocks, AnthropicContentBlock{Type: "text", Text: doneText})
		}
		if hasCalls {
			stopReason = "tool_use"
			for _, tc := range toolCalls {
				contentBlocks = append(contentBlocks, AnthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
		}
	} else {
		if isJSONSchemaOutput(outputConfig) {
			content = normalizeStructuredOutputText(content)
		}
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type: "text",
			Text: cleanCitationsWithContext(content, knownToolCallURLs, knownCitationURLs, knownCitationDocs),
		})
	}
	// WebSearch may have added a second inference step after the initial
	// usage snapshot was created. Report the final peak-input/summed-output
	// values actually used by the complete request.
	if finalUsage != nil {
		aUsage.InputTokens = reportedInputTokens(finalUsage.PromptTokens, requestDiagnostic)
		aUsage.OutputTokens = finalUsage.CompletionTokens
	}

	resp := AnthropicResponse{
		ID:         requestID,
		Type:       "message",
		Role:       "assistant",
		Content:    contentBlocks,
		Model:      model,
		StopReason: &stopReason,
		Usage:      aUsage,
	}
	if requestDiagnostic != nil {
		if stopReason == "tool_use" {
			requestDiagnostic.SetFinishReason("tool_calls")
		} else {
			requestDiagnostic.SetFinishReason("stop")
		}
	}

	session.publishAssistantContinuation(continuationMessageFromBlocks(contentBlocks))
	LogAPIOutputJSON(requestID, "anthropic non-stream response", resp)
	w.Header().Set("Content-Type", anthropicJSONContentType)
	json.NewEncoder(w).Encode(resp)
	return nil
}

func sendAnthropicSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) {
	raw, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, raw)
	flusher.Flush()
}

func writeAnthropicStreamError(w http.ResponseWriter, flusher http.Flusher, requestID string, cause error) error {
	log.Printf("[err] %s: upstream stream interrupted after partial output: %v", requestID, cause)
	payload := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    "api_error",
			"message": "upstream stream interrupted: " + cause.Error(),
		},
	}
	LogAPIOutputJSON(requestID, "anthropic stream error", payload)
	sendAnthropicSSE(w, flusher, "error", payload)
	return fmt.Errorf("%w: %w", ErrStreamResponseStarted, cause)
}

// truncateForLog truncates by rune count to avoid splitting UTF-8 sequences.
func truncateForLog(s string, maxRunes int) string {
	if s == "" || maxRunes <= 0 {
		return ""
	}
	runes := 0
	for i := range s {
		if runes == maxRunes {
			return s[:i] + "..."
		}
		runes++
	}
	return s
}

func strPtr(s string) *string {
	return &s
}

// logAnthropicRequest logs the raw Anthropic request details
func logAnthropicRequest(req AnthropicRequest, model string) {
	if !DebugLoggingEnabled() {
		return
	}

	log.Printf("[debug] ╔══ Anthropic Request ══╗")
	log.Printf("[debug] ║ model=%s max_tokens=%d stream=%v", model, req.MaxTokens, req.Stream)

	// Log system prompt
	if req.System != nil {
		switch s := req.System.(type) {
		case string:
			preview := truncateForLog(s, 200)
			log.Printf("[debug] ║ system(%d chars): %s", len(s), preview)
			if AppConfig.Server.DumpAPIInput {
				os.WriteFile("claude_code_system_dump.txt", []byte(s), 0644)
				log.Printf("[debug] ║ system dumped to claude_code_system_dump.txt")
			}
		case []interface{}:
			log.Printf("[debug] ║ system: %d content blocks", len(s))
			if AppConfig.Server.DumpAPIInput {
				sysDump, _ := json.MarshalIndent(s, "", "  ")
				os.WriteFile("claude_code_system_dump.json", sysDump, 0644)
				log.Printf("[debug] ║ system dumped to claude_code_system_dump.json")
			}
		}
	}

	// Log tools
	if len(req.Tools) > 0 {
		log.Printf("[debug] ║ tools(%d):", len(req.Tools))
		for _, t := range req.Tools {
			log.Printf("[debug] ║   - %s: %s", t.Name, t.Description)
		}
		if AppConfig.Server.DumpAPIInput {
			toolDump, _ := json.MarshalIndent(req.Tools, "", "  ")
			os.WriteFile("claude_code_tools_dump.json", toolDump, 0644)
			log.Printf("[debug] ║ tools dumped to claude_code_tools_dump.json (%d bytes)", len(toolDump))
		}
	}

	// Log tool_choice
	if req.ToolChoice != nil {
		tcRaw, _ := json.Marshal(req.ToolChoice)
		log.Printf("[debug] ║ tool_choice: %s", string(tcRaw))
	}

	// Log messages
	log.Printf("[debug] ║ messages(%d):", len(req.Messages))
	for i, msg := range req.Messages {
		switch content := msg.Content.(type) {
		case string:
			preview := truncateForLog(content, 200)
			log.Printf("[debug] ║   [%d] role=%s text(%d): %s", i, msg.Role, len(content), preview)
		case []interface{}:
			var blockTypes []string
			for _, block := range content {
				if m, ok := block.(map[string]interface{}); ok {
					bt, _ := m["type"].(string)
					switch bt {
					case "tool_use":
						name, _ := m["name"].(string)
						blockTypes = append(blockTypes, fmt.Sprintf("tool_use(%s)", name))
					case "tool_result":
						tuID, _ := m["tool_use_id"].(string)
						blockTypes = append(blockTypes, fmt.Sprintf("tool_result(%s)", tuID))
					case "text":
						text, _ := m["text"].(string)
						text = truncateForLog(text, 80)
						blockTypes = append(blockTypes, fmt.Sprintf("text(%d chars)", len(text)))
					default:
						blockTypes = append(blockTypes, bt)
					}
				}
			}
			log.Printf("[debug] ║   [%d] role=%s blocks=[%s]", i, msg.Role, strings.Join(blockTypes, ", "))
		}
	}
	log.Printf("[debug] ╚═══════════════════════╝")
}

// logConvertedMessages logs the internal ChatMessage format after conversion
func logConvertedMessages(messages []ChatMessage) {
	if !DebugLoggingEnabled() {
		return
	}

	log.Printf("[debug] === Converted to %d internal messages ===", len(messages))
	for i, m := range messages {
		preview := truncateForLog(m.Content, 200)
		extra := ""
		if len(m.ToolCalls) > 0 {
			var names []string
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			extra = fmt.Sprintf(" tool_calls=[%s]", strings.Join(names, ","))
		}
		if m.ToolCallID != "" {
			extra += fmt.Sprintf(" tool_call_id=%s name=%s", m.ToolCallID, m.Name)
		}
		log.Printf("[debug]   [%d] role=%-9s len=%-5d%s: %s", i, m.Role, len(m.Content), extra, preview)
	}
}

// generateFakeSignature creates a synthetic base64 signature for thinking blocks.
// Real Claude API uses cryptographic signatures for integrity verification.
// CC may or may not validate these — if it does, this will need adjustment.
func generateFakeSignature() string {
	b := make([]byte, 96)
	for i := range b {
		b[i] = byte(i*37 + 13) // deterministic pseudo-random fill
	}
	return "EqQB" + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b)
}

// handleResearcherStream handles streaming Anthropic response for researcher mode.
//
// Two modes depending on whether the client enabled thinking:
//   - hasThinking=true:  thinking block (research steps) → text block (report)
//   - hasThinking=false: single text block (research steps + separator + report)
//
// SSE headers are deferred until the first actual data arrives, so that quota-
// exhaustion retries don't produce duplicate headers.
func handleResearcherStream(ctx context.Context, w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasThinking bool, requestDiagnostic *RequestDiagnostic) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, requestID, http.StatusInternalServerError, "streaming not supported", "api_error")
		return nil
	}

	var finalUsage *UsageInfo
	defer func() {
		recordCompletedAttemptUsage(acc, model, finalUsage, requestDiagnostic)
	}()
	var thinkingForLog strings.Builder
	var textForLog strings.Builder
	blockIndex := 0
	thinkingBlockOpen := false
	textBlockStarted := false
	headersSent := false
	// When !hasThinking, research steps are streamed as text; this tracks whether
	// a separator has been emitted between steps and the report.
	researchTextEmitted := false

	// ensureHeaders writes SSE headers + message_start on the first data event.
	ensureHeaders := func() {
		if headersSent {
			return
		}
		headersSent = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		sendAnthropicSSE(w, flusher, "message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            requestID,
				"type":          "message",
				"role":          "assistant",
				"content":       []interface{}{},
				"model":         model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]interface{}{"input_tokens": 500, "output_tokens": 0},
			},
		})
		sendAnthropicSSE(w, flusher, "ping", map[string]string{"type": "ping"})
	}

	// ensureTextBlock opens a text block if not already open
	ensureTextBlock := func() {
		ensureHeaders()
		if !textBlockStarted {
			sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         blockIndex,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			textBlockStarted = true
		}
	}

	// sendTextDelta sends a text delta SSE event
	sendTextDelta := func(text string) {
		textForLog.WriteString(text)
		sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]interface{}{"type": "text_delta", "text": text},
		})
	}

	callOpts := CallOptions{
		Context:           ctx,
		IsResearcher:      true,
		RequestID:         requestID,
		RequestDiagnostic: requestDiagnostic,
	}

	// Set up ThinkingCallback — always needed for researcher mode
	callOpts.ThinkingCallback = func(delta string, done bool, signature string) {
		if hasThinking {
			// Client supports thinking: emit as thinking block
			if done {
				if thinkingBlockOpen {
					sig := signature
					if sig == "" {
						sig = generateFakeSignature()
					}
					sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": blockIndex,
						"delta": map[string]interface{}{"type": "signature_delta", "signature": sig},
					})
					sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
						"type": "content_block_stop", "index": blockIndex,
					})
					blockIndex++
					thinkingBlockOpen = false
					log.Printf("[researcher] closed thinking block (real_sig=%v)", signature != "")
				}
				return
			}
			if delta == "" {
				return
			}
			thinkingForLog.WriteString(delta)
			ensureHeaders()
			if !thinkingBlockOpen {
				sendAnthropicSSE(w, flusher, "content_block_start", map[string]interface{}{
					"type":          "content_block_start",
					"index":         blockIndex,
					"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
				})
				thinkingBlockOpen = true
				log.Printf("[researcher] opened thinking block")
			}
			sendAnthropicSSE(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": delta},
			})
		} else {
			// Client doesn't support thinking: stream research steps as text
			if done {
				// Add separator between research steps and report
				if researchTextEmitted {
					ensureTextBlock()
					sendTextDelta("\n\n---\n\n")
				}
				return
			}
			if delta == "" {
				return
			}
			ensureTextBlock()
			sendTextDelta(delta)
			researchTextEmitted = true
		}
	}

	cbErr := CallInference(acc, messages, model, false, func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			ensureTextBlock()
			sendTextDelta(delta)
		}
		if usage != nil {
			finalUsage = usage
		}
	}, callOpts)

	if cbErr != nil {
		log.Printf("[err] %s researcher: %v", requestID, cbErr)
		if headersSent {
			return writeAnthropicStreamError(w, flusher, requestID, cbErr)
		}
		if errors.Is(cbErr, ErrResearchQuotaExhausted) {
			return cbErr
		}
		return cbErr
	}

	// Close text block if started
	if textBlockStarted {
		sendAnthropicSSE(w, flusher, "content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": blockIndex,
		})
		blockIndex++
	}

	// message_delta + message_stop
	ensureHeaders()
	outputTokens := 0
	if finalUsage != nil {
		outputTokens = finalUsage.CompletionTokens
	}
	sendAnthropicSSE(w, flusher, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": outputTokens},
	})
	sendAnthropicSSE(w, flusher, "message_stop", map[string]string{"type": "message_stop"})

	var contentBlocks []AnthropicContentBlock
	if hasThinking && thinkingForLog.Len() > 0 {
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type:      "thinking",
			Thinking:  thinkingForLog.String(),
			Signature: generateFakeSignature(),
		})
	}
	if textForLog.Len() > 0 {
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type: "text",
			Text: textForLog.String(),
		})
	}
	LogAPIOutputJSON(requestID, "anthropic researcher stream summary", AnthropicResponse{
		ID:         requestID,
		Type:       "message",
		Role:       "assistant",
		Content:    contentBlocks,
		Model:      model,
		StopReason: strPtr("end_turn"),
		Usage: &AnthropicUsage{
			InputTokens: func() int {
				if finalUsage != nil {
					return finalUsage.PromptTokens
				}
				return 0
			}(),
			OutputTokens: outputTokens,
		},
	})

	log.Printf("[researcher] %s complete: thinking_mode=%v, text_streamed=%v", requestID, hasThinking, textBlockStarted)
	return nil
}

// handleResearcherNonStream handles non-streaming Anthropic response for researcher mode.
// Collects all content first, then returns a complete JSON response.
func handleResearcherNonStream(ctx context.Context, w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasThinking bool, requestDiagnostic *RequestDiagnostic) error {
	var fullContent strings.Builder
	var finalUsage *UsageInfo
	defer func() {
		recordCompletedAttemptUsage(acc, model, finalUsage, requestDiagnostic)
	}()
	var thinkingBlocks []ThinkingBlock

	callOpts := CallOptions{
		Context:           ctx,
		IsResearcher:      true,
		ThinkingBlocks:    &thinkingBlocks,
		RequestID:         requestID,
		RequestDiagnostic: requestDiagnostic,
	}

	cbErr := CallInference(acc, messages, model, false, func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			fullContent.WriteString(delta)
		}
		if usage != nil {
			finalUsage = usage
		}
	}, callOpts)

	if cbErr != nil {
		if errors.Is(cbErr, ErrResearchQuotaExhausted) {
			return cbErr
		}
		log.Printf("[err] %s researcher non-stream: %v", requestID, cbErr)
		return cbErr
	}

	aUsage := &AnthropicUsage{}
	if finalUsage != nil {
		aUsage.InputTokens = finalUsage.PromptTokens
		aUsage.OutputTokens = finalUsage.CompletionTokens
	}

	var contentBlocks []AnthropicContentBlock
	if hasThinking && len(thinkingBlocks) > 0 {
		for _, tb := range thinkingBlocks {
			sig := tb.Signature
			if sig == "" {
				sig = generateFakeSignature()
			}
			contentBlocks = append(contentBlocks, AnthropicContentBlock{
				Type:      "thinking",
				Thinking:  tb.Content,
				Signature: sig,
			})
		}
	}
	contentBlocks = append(contentBlocks, AnthropicContentBlock{
		Type: "text",
		Text: fullContent.String(),
	})

	stopReason := "end_turn"
	resp := AnthropicResponse{
		ID:         requestID,
		Type:       "message",
		Role:       "assistant",
		Content:    contentBlocks,
		Model:      model,
		StopReason: &stopReason,
		Usage:      aUsage,
	}

	LogAPIOutputJSON(requestID, "anthropic researcher non-stream response", resp)
	w.Header().Set("Content-Type", anthropicJSONContentType)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)

	log.Printf("[researcher] %s non-stream complete: %d thinking blocks, %d chars text", requestID, len(thinkingBlocks), fullContent.Len())
	return nil
}

// recordCompletedAttemptUsage feeds both the existing aggregate counters and
// the metadata-only per-request diagnostic. It is called once per Notion
// attempt after the final cumulative usage value is known.
func recordCompletedAttemptUsage(acc *Account, model string, usage *UsageInfo, requestDiagnostic *RequestDiagnostic) {
	if usage == nil || acc == nil {
		return
	}
	GlobalUsageStats().Record(acc.UserEmail, model, usage.PromptTokens, usage.CompletionTokens)
	if requestDiagnostic != nil {
		requestDiagnostic.AddUsage(usage.PromptTokens, usage.CompletionTokens)
	}
}

func reportedInputTokens(actual int, requestDiagnostic *RequestDiagnostic) int {
	if actual < 0 {
		actual = 0
	}
	if requestDiagnostic != nil {
		requestDiagnostic.SetContextTokens(actual)
	}
	return actual
}

func writeAnthropicError(w http.ResponseWriter, requestID string, status int, message, errType string) {
	markRequestDiagnosticError(w, status, message)
	payload := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	}
	LogAPIOutputJSON(requestID, fmt.Sprintf("anthropic error status=%d", status), payload)

	w.Header().Set("Content-Type", anthropicJSONContentType)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
