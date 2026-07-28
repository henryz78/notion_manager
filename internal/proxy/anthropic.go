package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

var ErrToolBridgeNoTool = errors.New("tool bridge produced no usable tool action")

const maxInferenceAccountCalls = 3
const maxEmptyResponseAttempts = 2

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
func streamWebSearch(w http.ResponseWriter, flusher http.Flusher, acc *Account, query string, model string, requestID string, blockIndex *int, hasThinking bool) (*UsageInfo, error) {
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
		EnableWebSearch:   true,
		ThinkingBlocks:    &thinkingBlocks,
		KnownCitationURLs: &knownCitationURLs,
		KnownCitationDocs: &knownCitationDocs,
		KnownToolCallURLs: &knownToolCallURLs,
		RequestID:         requestID,
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

	return finalUsage, err
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

		if r.Method != http.MethodPost {
			writeAnthropicError(w, requestID, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
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
		messages, fileAttachments := convertAnthropicMessages(req.System, req.Messages)
		if err := validateToolProtocol(messages); err != nil {
			writeAnthropicError(w, requestID, http.StatusBadRequest, "invalid tool history: "+err.Error(), "invalid_request_error")
			return
		}
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
		allowedToolNames := declaredToolNames(req.Tools)
		enableWebSearch := effectiveWebSearch

		if requestDiagnostic != nil {
			state := "full_replay"
			if isResearcher {
				state = "not_applicable"
			}
			requestDiagnostic.SetContextMode(state)
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
			// Filter out WebSearch/WebFetch — these are handled by Notion's native search.
			// Injecting them as custom tools causes the model to generate JSON tool calls
			// instead of using Notion's server-side search which actually executes.
			var toolDetectedWebSearch bool
			convertedTools, toolDetectedWebSearch = filterNativeSearchTools(convertedTools)
			if toolDetectedWebSearch {
				enableWebSearch = true
				log.Printf("[bridge] WebSearch/WebFetch detected — enabling Notion native search and preserving history")
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

		// Snapshot the original (pre-injection) messages so failover to a
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
		liveCheckInterval := AppConfig.QuotaLiveCheckInterval()

		for selection := 0; selection < selectionLimit && accountCalls < maxAccountCalls; selection++ {
			var acc *Account

			if acc == nil {
				if isResearcher {
					if selection == 0 {
						acc = pool.NextForResearch()
					} else {
						// Research-mode fallback also rotates through the pool.
						acc = pool.NextExcluding(tried)
					}
				} else if selection == 0 {
					// New-conversation routing: prefer full Notion AI plans,
					// then live premium signals, without adding undocumented
					// private counters together.
					acc = pool.NextBest()
				} else {
					// Failover: keep the same service-tier preference among
					// accounts we haven't tried yet.
					acc = pool.NextBestExcluding(tried)
				}
			}
			if acc == nil {
				break
			}

			// Live quota pre-check: ensure the cached state is fresh enough
			// that we don't waste an inference call on an exhausted account.
			// Researcher mode has its own picker that already inspects quota.
			if !isResearcher && !pool.RefreshAccountQuota(acc, liveCheckInterval) {
				log.Printf("[quota-live] %s skipped (exhausted on live check)", acc.UserEmail)
				tried[acc] = true
				pool.MarkQuotaExhausted(acc)
				continue
			}
			tried[acc] = true

			// Build the request payload for this attempt. We always start
			// from the pristine `originalMessages` snapshot so a per-attempt
			// tool injection (which mutates messages in place for large tool
			// sets) cannot leak into subsequent retries on a different
			// account.
			attemptMessages := cloneChatMessages(originalMessages)
			if hasTools {
				attemptMessages = aliasToolNamesInMessages(attemptMessages, originalToToolAlias)
				attemptMessages = injectToolsIntoMessages(attemptMessages, bridgeTools, aliasToolChoice(req.ToolChoice, originalToToolAlias))
				if DebugLoggingEnabled() && accountCalls == 0 {
					log.Printf("[debug] === After tool injection (%d messages) ===", len(attemptMessages))
					for i, m := range attemptMessages {
						preview := truncateForLog(m.Content, 300)
						log.Printf("[debug]   [%d] role=%s toolcalls=%d content_len=%d: %s",
							i, m.Role, len(m.ToolCalls), len(m.Content), preview)
					}
				}
			}

			requestMessages := attemptMessages

			accountCalls++
			log.Printf("[req] %s model=%s messages=%d stream=%v tools=%d attachments=%d account=%s full_replay=true (attempt %d/%d) [anthropic]",
				requestID, model, len(req.Messages), req.Stream, len(req.Tools), len(fileAttachments), acc.UserEmail, accountCalls, maxAccountCalls)
			if requestDiagnostic != nil {
				requestDiagnostic.BeginAttempt(acc.UserEmail)
			}

			// Upload file attachments to Notion (if any) — skip for researcher mode
			var uploadedAttachments []UploadedAttachment
			if !isResearcher && len(fileAttachments) > 0 {
				for i, fa := range fileAttachments {
					log.Printf("[upload-debug] %s: uploading attachment %d/%d: %s (%s, %d bytes)",
						requestID, i+1, len(fileAttachments), fa.FileName, fa.ContentType, len(fa.Data))
					uploaded, err := UploadFileToNotion(acc, &fa)
					if err != nil {
						log.Printf("[upload] %s: attachment %d upload failed: %v", requestID, i+1, err)
						if requestDiagnostic != nil {
							requestDiagnostic.FinishAttempt("upload_error", err)
						}
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
					reqErr = handleResearcherStream(w, acc, requestMessages, model, requestID, hasThinking, requestDiagnostic)
				} else {
					reqErr = handleResearcherNonStream(w, acc, requestMessages, model, requestID, hasThinking, requestDiagnostic)
				}
			} else if req.Stream {
				reqErr = handleAnthropicStream(w, acc, requestMessages, model, requestID, hasTools, len(bridgeTools) > 0, allowedToolNames, toolAliasToOriginal, toolChoiceMode, hasThinking, enableWebSearch, enableWorkspaceSearch, useReadOnlyMode, uploadedAttachments, req.OutputConfig, requestDiagnostic)
			} else {
				reqErr = handleAnthropicNonStream(w, acc, requestMessages, model, requestID, hasTools, len(bridgeTools) > 0, allowedToolNames, toolAliasToOriginal, toolChoiceMode, hasThinking, enableWebSearch, enableWorkspaceSearch, useReadOnlyMode, uploadedAttachments, req.OutputConfig, requestDiagnostic)
			}
			if requestDiagnostic != nil {
				requestDiagnostic.FinishAttempt(requestAttemptOutcome(reqErr), reqErr)
				if reqErr == nil && !hasTools {
					requestDiagnostic.SetToolBridge("none")
					requestDiagnostic.SetFinishReason("stop")
				}
			}

			// Trigger an async live quota refresh after every call so the next
			// selection has up-to-date numbers. Deduplicated per account so
			// concurrent calls don't trigger redundant Notion API hits.
			pool.RefreshAccountQuotaAsync(acc)

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
			if reqErr != nil && errors.Is(reqErr, ErrQuotaExhausted) {
				log.Printf("[quota] %s quota exhausted — retaining account for future re-check", acc.UserEmail)
				pool.MarkQuotaExhausted(acc)
				log.Printf("[quota] %s quota exhausted, trying next account (%d/%d available)",
					acc.UserEmail, pool.AvailableCount(), pool.Count())
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
				if emptyResponseCount >= maxEmptyResponseAttempts {
					break
				}
				continue
			}

			if reqErr != nil && errors.Is(reqErr, ErrPremiumFeatureUnavailable) {
				// Premium feature unavailable — for free accounts this means quota is permanently gone
				if isFreePlan(acc) {
					log.Printf("[premium] %s complimentary trial unavailable — retaining account for future re-check", acc.UserEmail)
					pool.MarkPermanentlyExhausted(acc)
				} else {
					log.Printf("[premium] %s premium feature unavailable, trying next account", acc.UserEmail)
					pool.MarkTemporarilyUnavailable(acc, "premium_unavailable", defaultAccountFailureCooldown)
				}
				if requestDiagnostic != nil {
					requestDiagnostic.SetContextMode("full_replay_account_switch")
				}
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
						continue
					}
					if shouldQuarantineAccountFailure(failure.Reason) {
						pool.MarkTemporarilyUnavailable(acc, failure.Reason, defaultAccountFailureCooldown)
					}
					log.Printf("[health] %s failed with %s, trying next account: %v", acc.UserEmail, failure.Reason, reqErr)
					if requestDiagnostic != nil {
						requestDiagnostic.SetContextMode("full_replay_after_error")
					}
					continue
				}
				break
			}

			if reqErr == nil {
				pool.ClearTemporaryUnavailable(acc)
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
	case errors.Is(err, ErrQuotaExhausted):
		return "quota_exhausted"
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
func convertAnthropicMessages(system interface{}, msgs []AnthropicMessage) ([]ChatMessage, []FileAttachment) {
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

	for _, msg := range msgs {
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
							fa := extractFileFromSource(source, "image")
							if fa != nil {
								attachments = append(attachments, *fa)
							}
						}
					case "document":
						// Extract PDF/text document from base64, URL, or file source
						if source, ok := m["source"].(map[string]interface{}); ok {
							fa := extractFileFromSource(source, "document")
							if fa != nil {
								attachments = append(attachments, *fa)
							}
						}
					case "tool_result":
						// Convert tool_result to tool role message
						toolUseID, _ := m["tool_use_id"].(string)
						var resultText string
						if c, ok := m["content"].(string); ok {
							resultText = c
						} else if c, ok := m["content"].([]interface{}); ok {
							for _, cb := range c {
								if cbm, ok := cb.(map[string]interface{}); ok {
									if t, ok := cbm["text"].(string); ok {
										resultText += t
									}
								}
							}
						}
						result = append(result, ChatMessage{
							Role:       "tool",
							Content:    resultText,
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

	return result, attachments
}

// extractFileFromSource extracts file data from an Anthropic content block source.
// Supports base64 and URL source types. blockKind is "image" or "document".
func extractFileFromSource(source map[string]interface{}, blockKind string) *FileAttachment {
	srcType, _ := source["type"].(string)

	switch srcType {
	case "base64":
		mediaType, _ := source["media_type"].(string)
		data64, _ := source["data"].(string)
		if data64 == "" {
			return nil
		}
		decoded, err := base64.StdEncoding.DecodeString(data64)
		if err != nil {
			log.Printf("[upload] failed to decode base64 %s: %v", blockKind, err)
			return nil
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
		}

	case "url":
		urlStr, _ := source["url"].(string)
		if urlStr == "" {
			return nil
		}
		// Download the file from URL
		resp, err := http.Get(urlStr)
		if err != nil {
			log.Printf("[upload] failed to download %s from URL: %v", blockKind, err)
			return nil
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("[upload] failed to read %s URL response: %v", blockKind, err)
			return nil
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
		}

	default:
		log.Printf("[upload] unsupported source type %q for %s block", srcType, blockKind)
		return nil
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
	sawContent := false
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
			sawContent = true
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
					sawContent = true
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
			if errors.Is(cbErr, ErrQuotaExhausted) {
				return cbErr
			}
			log.Printf("[err] %s: %v", requestID, cbErr)
			return cbErr
		}
		log.Printf("[err] %s: streaming completed with partial data before error: %v", requestID, cbErr)
	}

	if fullContent.Len() == 0 && !sawContent {
		log.Printf("[warn] %s: empty response from Notion, will retry", requestID)
		return ErrEmptyResponse
	}

	if thinkingBlockOpen {
		closeThinkingBlock("")
	}

	if !jsonOnlyOutput {
		if flushed := cr.Flush(); flushed != "" {
			sawContent = true
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
			sawContent = true
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
		text := renderAnthropicCitationText(fullContent.String(), knownCitationURLs, knownCitationDocs, knownToolCallURLs)
		if jsonOnlyOutput {
			text = normalizeStructuredOutputText(fullContent.String())
		}
		contentBlocks = append(contentBlocks, AnthropicContentBlock{
			Type: "text",
			Text: text,
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

func toolProtocolPrefixState(content string) (protocol, undecided bool) {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	if trimmed == "" {
		return false, true
	}
	for _, prefix := range []string{"{", "<|", "<tool_call>", "```"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true, false
		}
		if strings.HasPrefix(prefix, trimmed) {
			return false, true
		}
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
func handleAnthropicStream(w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasTools, hasBridgedClientTools bool, allowedToolNames map[string]struct{}, toolAliasToOriginal map[string]string, toolChoiceMode string, hasThinking bool, enableWebSearch bool, enableWorkspaceSearch *bool, useReadOnlyMode bool, attachments []UploadedAttachment, outputConfig *AnthropicOutputConfig, requestDiagnostic *RequestDiagnostic) error {
	if !hasTools {
		callOpts := CallOptions{
			HasClientTools:        hasBridgedClientTools,
			EnableWebSearch:       enableWebSearch,
			EnableWorkspaceSearch: enableWorkspaceSearch,
			UseReadOnlyMode:       useReadOnlyMode,
			Attachments:           attachments,
			RequestID:             requestID,
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
		log.Printf("[err] %s: tool stream ended after partial output: %v", requestID, cbErr)
	}

	content := fullContent.String()
	if content == "" && len(nativeToolUses) == 0 && thinkingForLog.Len() == 0 {
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
		searchUsage, searchErr := streamWebSearch(w, flusher, acc, prepared.WebSearchQuery, model, requestID, &blockIndex, hasThinking)
		if searchErr != nil {
			log.Printf("[bridge] WebSearch streaming failed: %v", searchErr)
		}
		if searchUsage != nil && finalUsage != nil {
			finalUsage.PromptTokens += searchUsage.PromptTokens
			finalUsage.CompletionTokens += searchUsage.CompletionTokens
			finalUsage.TotalTokens = finalUsage.PromptTokens + finalUsage.CompletionTokens
		}
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
func handleAnthropicNonStream(w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasTools, hasBridgedClientTools bool, allowedToolNames map[string]struct{}, toolAliasToOriginal map[string]string, toolChoiceMode string, hasThinking bool, enableWebSearch bool, enableWorkspaceSearch *bool, useReadOnlyMode bool, attachments []UploadedAttachment, outputConfig *AnthropicOutputConfig, requestDiagnostic *RequestDiagnostic) error {
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
		// Format-based injection: tools embedded in user messages, use normal chat path
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
		if errors.Is(err, ErrQuotaExhausted) {
			return err
		}
		log.Printf("[err] %s: %v", requestID, err)
		return err
	}

	content := fullContent.String()

	// Empty response: Notion returned 200 but produced no text — retry on next account
	if len(content) == 0 && len(nativeToolUses) == 0 {
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
			searchResult, searchUsage, searchErr := executeWebSearch(acc, prepared.WebSearchQuery, model, requestID)
			if searchErr == nil && searchResult != "" {
				if doneText != "" {
					doneText = doneText + "\n\n" + searchResult
				} else {
					doneText = searchResult
				}
				if searchUsage != nil && finalUsage != nil {
					finalUsage.PromptTokens += searchUsage.PromptTokens
					finalUsage.CompletionTokens += searchUsage.CompletionTokens
					finalUsage.TotalTokens = finalUsage.PromptTokens + finalUsage.CompletionTokens
				}
			} else if searchErr != nil {
				log.Printf("[bridge] WebSearch execution failed: %v", searchErr)
				if doneText != "" {
					doneText = doneText + "\n\nWeb search failed: " + searchErr.Error()
				} else {
					doneText = "Web search failed: " + searchErr.Error()
				}
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

	LogAPIOutputJSON(requestID, "anthropic non-stream response", resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	return nil
}

func sendAnthropicSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) {
	raw, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, raw)
	flusher.Flush()
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
func handleResearcherStream(w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasThinking bool, requestDiagnostic *RequestDiagnostic) error {
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
		if errors.Is(cbErr, ErrQuotaExhausted) || errors.Is(cbErr, ErrResearchQuotaExhausted) {
			return cbErr
		}
		log.Printf("[err] %s researcher: %v", requestID, cbErr)
		if !headersSent {
			writeAnthropicError(w, requestID, http.StatusBadGateway, "notion researcher error: "+cbErr.Error(), "api_error")
		}
		return nil
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
func handleResearcherNonStream(w http.ResponseWriter, acc *Account, messages []ChatMessage, model, requestID string, hasThinking bool, requestDiagnostic *RequestDiagnostic) error {
	var fullContent strings.Builder
	var finalUsage *UsageInfo
	defer func() {
		recordCompletedAttemptUsage(acc, model, finalUsage, requestDiagnostic)
	}()
	var thinkingBlocks []ThinkingBlock

	callOpts := CallOptions{
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
		if errors.Is(cbErr, ErrQuotaExhausted) || errors.Is(cbErr, ErrResearchQuotaExhausted) {
			return cbErr
		}
		log.Printf("[err] %s researcher non-stream: %v", requestID, cbErr)
		writeAnthropicError(w, requestID, http.StatusBadGateway, "notion researcher error: "+cbErr.Error(), "api_error")
		return nil
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
	w.Header().Set("Content-Type", "application/json")
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
