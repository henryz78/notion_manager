package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestHistoryLimit    = 100
	defaultRequestHistoryPageSize = 50
	maxRequestHistoryPageSize     = 200
	maxRequestHistoryErrorLength  = 500

	RequestPromptModeExisting             = "existing_prompt"
	RequestPromptModePersonalInstructions = "notion_personal_instructions"
	RequestPromptModeClientAndPersonal    = "client_and_notion_personal"
	RequestPromptModeNone                 = "no_behavior_prompt"
	RequestPromptModeNotApplicable        = "not_applicable"
)

func currentRequestPromptMode() string {
	clientPrompt := AppConfig.ClientSystemPromptEnabled()
	personalInstructions := AppConfig.NotionPersonalInstructionsEnabled()
	switch {
	case clientPrompt && personalInstructions:
		return RequestPromptModeClientAndPersonal
	case clientPrompt:
		return RequestPromptModeExisting
	case personalInstructions:
		return RequestPromptModePersonalInstructions
	default:
		return RequestPromptModeNone
	}
}

// RequestHistoryEntry contains diagnostic metadata for one client API request.
// It deliberately has no fields for messages, prompts, tool arguments, model
// output, or Notion personal-instruction content.
type RequestHistoryEntry struct {
	ID               string           `json:"id"`
	CreatedAt        time.Time        `json:"created_at"`
	API              string           `json:"api"`
	RequestedModel   string           `json:"requested_model"`
	UsedDefaultModel bool             `json:"used_default_model,omitempty"`
	NotionModel      string           `json:"notion_model,omitempty"`
	AccountEmail     string           `json:"account_email,omitempty"`
	PromptMode       string           `json:"prompt_mode"`
	ToolCount        int              `json:"tool_count"`
	Stream           bool             `json:"stream"`
	ToolChoice       string           `json:"tool_choice,omitempty"`
	ToolBridge       string           `json:"tool_bridge,omitempty"`
	FinishReason     string           `json:"finish_reason,omitempty"`
	ContextMode      string           `json:"context_mode,omitempty"`
	InputTokens      int              `json:"input_tokens"`
	ContextTokens    int              `json:"context_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	DurationMs       int64            `json:"duration_ms"`
	Status           string           `json:"status"`
	HTTPStatus       int              `json:"http_status"`
	Error            string           `json:"error,omitempty"`
	Attempts         int              `json:"attempts"`
	AttemptDetails   []RequestAttempt `json:"attempt_details,omitempty"`
}

type RequestAttempt struct {
	AccountEmail string `json:"account_email"`
	Outcome      string `json:"outcome,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	startedAt    time.Time
}

type requestHistoryFile struct {
	Entries []RequestHistoryEntry `json:"entries"`
}

// RequestHistoryStore keeps a bounded, concurrency-safe history and persists
// it as JSON. Entries are stored oldest-first internally and returned
// newest-first to the Dashboard.
type RequestHistoryStore struct {
	mu     sync.RWMutex
	saveMu sync.Mutex

	entries    []RequestHistoryEntry
	path       string
	maxEntries int
	revision   uint64
	savedAtRev uint64
}

// NewRequestHistoryStore loads an existing history file when present.
func NewRequestHistoryStore(path string, maxEntries int) (*RequestHistoryStore, error) {
	if maxEntries <= 0 {
		maxEntries = defaultRequestHistoryLimit
	}
	store := &RequestHistoryStore{
		path:       path,
		maxEntries: maxEntries,
		entries:    []RequestHistoryEntry{},
	}
	if path == "" {
		return store, nil
	}
	if err := store.Load(path); err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return store, err
	}
	return store, nil
}

// Record appends one completed request and drops the oldest rows above the cap.
func (s *RequestHistoryStore) Record(entry RequestHistoryEntry) {
	if s == nil {
		return
	}
	entry.Error = sanitizeRequestHistoryError(entry.Error)
	if entry.ToolCount < 0 {
		entry.ToolCount = 0
	}
	if entry.InputTokens < 0 {
		entry.InputTokens = 0
	}
	if entry.ContextTokens < 0 {
		entry.ContextTokens = 0
	}
	if entry.OutputTokens < 0 {
		entry.OutputTokens = 0
	}
	if entry.Attempts < 0 {
		entry.Attempts = 0
	}

	s.mu.Lock()
	s.entries = append(s.entries, entry)
	if overflow := len(s.entries) - s.maxEntries; overflow > 0 {
		copy(s.entries, s.entries[overflow:])
		s.entries = s.entries[:len(s.entries)-overflow]
	}
	s.revision++
	s.mu.Unlock()
}

// Clear removes all rows. Call Save after Clear when immediate persistence is
// required (the Dashboard DELETE handler does this).
func (s *RequestHistoryStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = []RequestHistoryEntry{}
	s.revision++
	s.mu.Unlock()
}

// RequestHistoryQuery controls Dashboard filtering and pagination.
type RequestHistoryQuery struct {
	Page       int
	PageSize   int
	Status     string
	API        string
	PromptMode string
	Query      string
}

// RequestHistoryPage is the JSON response returned by /admin/request-history.
type RequestHistoryPage struct {
	Total         int                   `json:"total"`
	FilteredTotal int                   `json:"filtered_total"`
	Page          int                   `json:"page"`
	PageSize      int                   `json:"page_size"`
	Entries       []RequestHistoryEntry `json:"entries"`
}

// Snapshot returns a filtered newest-first page without exposing internal
// slices to callers.
func (s *RequestHistoryStore) Snapshot(query RequestHistoryQuery) RequestHistoryPage {
	if query.Page < 0 {
		query.Page = 0
	}
	if query.PageSize <= 0 {
		query.PageSize = defaultRequestHistoryPageSize
	}
	if query.PageSize > maxRequestHistoryPageSize {
		query.PageSize = maxRequestHistoryPageSize
	}

	status := strings.ToLower(strings.TrimSpace(query.Status))
	api := strings.ToLower(strings.TrimSpace(query.API))
	promptMode := strings.ToLower(strings.TrimSpace(query.PromptMode))
	search := strings.ToLower(strings.TrimSpace(query.Query))

	s.mu.RLock()
	total := len(s.entries)
	filtered := make([]RequestHistoryEntry, 0, total)
	for i := len(s.entries) - 1; i >= 0; i-- {
		entry := s.entries[i]
		if status != "" && status != "all" && strings.ToLower(entry.Status) != status {
			continue
		}
		if api != "" && api != "all" && strings.ToLower(entry.API) != api {
			continue
		}
		if promptMode != "" && promptMode != "all" && strings.ToLower(entry.PromptMode) != promptMode {
			continue
		}
		if search != "" && !requestHistoryEntryMatches(entry, search) {
			continue
		}
		filtered = append(filtered, entry)
	}
	s.mu.RUnlock()

	start := query.Page * query.PageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + query.PageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	pageEntries := append([]RequestHistoryEntry(nil), filtered[start:end]...)
	if pageEntries == nil {
		pageEntries = []RequestHistoryEntry{}
	}

	return RequestHistoryPage{
		Total:         total,
		FilteredTotal: len(filtered),
		Page:          query.Page,
		PageSize:      query.PageSize,
		Entries:       pageEntries,
	}
}

func requestHistoryEntryMatches(entry RequestHistoryEntry, search string) bool {
	fields := []string{
		entry.ID,
		entry.RequestedModel,
		entry.NotionModel,
		entry.AccountEmail,
		entry.API,
		entry.PromptMode,
		entry.Status,
		entry.Error,
		entry.ToolChoice,
		entry.ToolBridge,
		entry.FinishReason,
		entry.ContextMode,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), search) {
			return true
		}
	}
	for _, attempt := range entry.AttemptDetails {
		if strings.Contains(strings.ToLower(attempt.AccountEmail), search) || strings.Contains(strings.ToLower(attempt.Outcome), search) {
			return true
		}
	}
	return false
}

// Load replaces the in-memory history from path.
func (s *RequestHistoryStore) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var file requestHistoryFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Entries == nil {
		file.Entries = []RequestHistoryEntry{}
	}
	if overflow := len(file.Entries) - s.maxEntries; overflow > 0 {
		file.Entries = append([]RequestHistoryEntry(nil), file.Entries[overflow:]...)
	}
	for i := range file.Entries {
		file.Entries[i].Error = sanitizeRequestHistoryError(file.Entries[i].Error)
	}

	s.mu.Lock()
	s.entries = file.Entries
	s.revision = 0
	s.savedAtRev = 0
	s.mu.Unlock()
	return nil
}

// Save writes the current bounded history atomically.
func (s *RequestHistoryStore) Save() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.RLock()
	revision := s.revision
	file := requestHistoryFile{
		Entries: append([]RequestHistoryEntry(nil), s.entries...),
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}

	s.mu.Lock()
	if s.revision == revision {
		s.savedAtRev = revision
	}
	s.mu.Unlock()
	return nil
}

func (s *RequestHistoryStore) isDirty() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision != s.savedAtRev
}

// StartFlushLoop periodically persists new rows. Railway sleeps only after an
// idle period, so a short interval keeps the most recent request durable
// without adding file I/O latency to each API response.
func (s *RequestHistoryStore) StartFlushLoop(interval time.Duration) func() {
	if s == nil || s.path == "" {
		return func() {}
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if s.isDirty() {
					if err := s.Save(); err != nil {
						log.Printf("[request-history] save %s: %v", s.path, err)
					}
				}
			case <-stop:
				if s.isDirty() {
					if err := s.Save(); err != nil {
						log.Printf("[request-history] final save %s: %v", s.path, err)
					}
				}
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// RequestDiagnostic is the mutable metadata collector attached to one HTTP
// request. It is shared with the OpenAI-to-Anthropic bridge through context.
type RequestDiagnostic struct {
	mu sync.Mutex

	entry    RequestHistoryEntry
	started  time.Time
	errorSet bool
}

func newRequestDiagnostic(api string) *RequestDiagnostic {
	started := time.Now()
	return &RequestDiagnostic{
		started: started,
		entry: RequestHistoryEntry{
			ID:         generateUUIDv4(),
			CreatedAt:  started.UTC(),
			API:        api,
			PromptMode: currentRequestPromptMode(),
			Status:     "success",
			HTTPStatus: http.StatusOK,
		},
	}
}

func (d *RequestDiagnostic) SetRequestedModel(model string, usedDefault bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.RequestedModel = strings.TrimSpace(model)
	d.entry.UsedDefaultModel = usedDefault
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetNotionModel(model string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.NotionModel = strings.TrimSpace(model)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetPromptMode(mode string) {
	if d == nil || strings.TrimSpace(mode) == "" {
		return
	}
	d.mu.Lock()
	d.entry.PromptMode = strings.TrimSpace(mode)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetToolCount(count int) {
	if d == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	d.mu.Lock()
	d.entry.ToolCount = count
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetClientRequest(stream bool, toolChoice interface{}, legacyChoice interface{}, toolCount int) {
	if d == nil {
		return
	}
	if toolCount < 0 {
		toolCount = 0
	}
	d.mu.Lock()
	d.entry.Stream = stream
	d.entry.ToolCount = toolCount
	d.entry.ToolChoice = requestToolChoiceMode(toolChoice, legacyChoice, toolCount)
	d.mu.Unlock()
}

func requestToolChoiceMode(toolChoice interface{}, legacyChoice interface{}, toolCount int) string {
	if toolCount <= 0 {
		return "none"
	}
	choice := toolChoice
	if choice == nil {
		choice = legacyChoice
	}
	if choice == nil {
		return "auto"
	}
	switch value := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "none":
			return "none"
		case "required", "any":
			return "required"
		case "auto":
			return "auto"
		default:
			return "forced"
		}
	case map[string]interface{}:
		if fn, ok := value["function"].(map[string]interface{}); ok {
			if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
				return "forced"
			}
		}
		if name, _ := value["name"].(string); strings.TrimSpace(name) != "" {
			return "forced"
		}
		if kind, _ := value["type"].(string); kind != "" {
			return requestToolChoiceMode(kind, nil, toolCount)
		}
	}
	return "auto"
}

func (d *RequestDiagnostic) SetToolBridge(protocol string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.ToolBridge = strings.TrimSpace(protocol)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetFinishReason(reason string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.FinishReason = strings.TrimSpace(reason)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetContextMode(mode string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.ContextMode = strings.TrimSpace(mode)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) BeginAttempt(accountEmail string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.entry.Attempts++
	d.entry.AccountEmail = strings.TrimSpace(accountEmail)
	d.entry.AttemptDetails = append(d.entry.AttemptDetails, RequestAttempt{
		AccountEmail: strings.TrimSpace(accountEmail),
		startedAt:    time.Now(),
	})
	d.mu.Unlock()
}

func (d *RequestDiagnostic) FinishAttempt(outcome string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.entry.AttemptDetails) - 1; i >= 0; i-- {
		attempt := &d.entry.AttemptDetails[i]
		if attempt.Outcome != "" {
			continue
		}
		attempt.Outcome = strings.TrimSpace(outcome)
		if !attempt.startedAt.IsZero() {
			attempt.DurationMs = time.Since(attempt.startedAt).Milliseconds()
		}
		return
	}
}

// AddUsage adds the final cumulative usage reported by one Notion attempt.
// Calling it once per attempt makes retry costs visible without double-counting
// repeated streaming callbacks from the same attempt.
func (d *RequestDiagnostic) AddUsage(input, output int) {
	if d == nil {
		return
	}
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	d.mu.Lock()
	d.entry.InputTokens += input
	d.entry.OutputTokens += output
	d.mu.Unlock()
}

func (d *RequestDiagnostic) SetContextTokens(tokens int) {
	if d == nil {
		return
	}
	if tokens < 0 {
		tokens = 0
	}
	d.mu.Lock()
	d.entry.ContextTokens = tokens
	d.mu.Unlock()
}

func (d *RequestDiagnostic) MarkError(status int, message string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.errorSet = true
	d.entry.Status = "error"
	if status > 0 {
		d.entry.HTTPStatus = status
	}
	d.entry.Error = sanitizeRequestHistoryError(message)
	d.mu.Unlock()
}

func (d *RequestDiagnostic) finish(httpStatus int) RequestHistoryEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	if httpStatus <= 0 {
		httpStatus = http.StatusOK
	}
	if !d.errorSet {
		d.entry.HTTPStatus = httpStatus
		if httpStatus >= http.StatusBadRequest {
			d.entry.Status = "error"
			d.entry.Error = fmt.Sprintf("HTTP %d", httpStatus)
		} else {
			d.entry.Status = "success"
		}
	}
	d.entry.DurationMs = time.Since(d.started).Milliseconds()
	d.entry.Error = sanitizeRequestHistoryError(d.entry.Error)
	for i := range d.entry.AttemptDetails {
		attempt := &d.entry.AttemptDetails[i]
		if attempt.Outcome == "" {
			if d.errorSet || httpStatus >= http.StatusBadRequest {
				attempt.Outcome = "error"
			} else {
				attempt.Outcome = "completed"
			}
			if !attempt.startedAt.IsZero() {
				attempt.DurationMs = time.Since(attempt.startedAt).Milliseconds()
			}
		}
	}
	return d.entry
}

func (d *RequestDiagnostic) ID() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.entry.ID
}

type requestDiagnosticContextKey struct{}

// RequestDiagnosticFromContext returns the current request collector.
func RequestDiagnosticFromContext(ctx context.Context) *RequestDiagnostic {
	if ctx == nil {
		return nil
	}
	diagnostic, _ := ctx.Value(requestDiagnosticContextKey{}).(*RequestDiagnostic)
	return diagnostic
}

// TrackRequestHistory records exactly one row around a public API handler.
// Internal OpenAI -> Anthropic bridge calls inherit the same collector and are
// intentionally not wrapped a second time.
func TrackRequestHistory(api string, store *RequestHistoryStore, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			next.ServeHTTP(w, r)
			return
		}

		diagnostic := newRequestDiagnostic(api)
		w.Header().Set("X-Request-ID", diagnostic.ID())
		base := &requestDiagnosticResponseWriter{
			ResponseWriter: w,
			diagnostic:     diagnostic,
		}
		var wrapped http.ResponseWriter = base
		if flusher, ok := w.(http.Flusher); ok {
			wrapped = &requestDiagnosticFlushingResponseWriter{
				requestDiagnosticResponseWriter: base,
				flusher:                         flusher,
			}
		}

		ctx := context.WithValue(r.Context(), requestDiagnosticContextKey{}, diagnostic)
		defer func() {
			if recovered := recover(); recovered != nil {
				diagnostic.MarkError(http.StatusInternalServerError, "internal server panic")
				store.Record(diagnostic.finish(http.StatusInternalServerError))
				panic(recovered)
			}
			store.Record(diagnostic.finish(base.statusCode()))
		}()
		next.ServeHTTP(wrapped, r.WithContext(ctx))
	}
}

type requestDiagnosticResponseWriter struct {
	http.ResponseWriter
	diagnostic *RequestDiagnostic
	status     int
	wrote      bool
}

func (w *requestDiagnosticResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestDiagnosticResponseWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *requestDiagnosticResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *requestDiagnosticResponseWriter) markRequestDiagnosticError(status int, message string) {
	w.diagnostic.MarkError(status, message)
}

type requestDiagnosticFlushingResponseWriter struct {
	*requestDiagnosticResponseWriter
	flusher http.Flusher
}

func (w *requestDiagnosticFlushingResponseWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	w.flusher.Flush()
}

type requestDiagnosticErrorMarker interface {
	markRequestDiagnosticError(status int, message string)
}

func markRequestDiagnosticError(w http.ResponseWriter, status int, message string) {
	if marker, ok := w.(requestDiagnosticErrorMarker); ok {
		marker.markRequestDiagnosticError(status, message)
	}
}

func sanitizeRequestHistoryError(message string) string {
	message = strings.TrimSpace(message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxRequestHistoryErrorLength {
		message = message[:maxRequestHistoryErrorLength] + "…"
	}
	return message
}

// HandleAdminRequestHistory serves the Dashboard history table.
func HandleAdminRequestHistory(store *RequestHistoryStore, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth != nil && auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		if store == nil {
			http.Error(w, `{"error":"request history is unavailable"}`, http.StatusServiceUnavailable)
			return
		}

		switch r.Method {
		case http.MethodGet:
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			result := store.Snapshot(RequestHistoryQuery{
				Page:       page,
				PageSize:   pageSize,
				Status:     r.URL.Query().Get("status"),
				API:        r.URL.Query().Get("api"),
				PromptMode: r.URL.Query().Get("prompt_mode"),
				Query:      r.URL.Query().Get("q"),
			})
			json.NewEncoder(w).Encode(result)
		case http.MethodDelete:
			store.Clear()
			if err := store.Save(); err != nil {
				log.Printf("[request-history] clear save %s: %v", store.path, err)
				http.Error(w, `{"error":"failed to persist cleared request history"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}
