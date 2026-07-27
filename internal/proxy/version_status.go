package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultVersionRepository = "henryz78/notion_manager"
	defaultVersionBranch     = "master"
	defaultVersionWorkflow   = "docker-image.yml"
)

type VersionStatus struct {
	CurrentVersion string    `json:"current_version"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	Status         string    `json:"status"`
	CheckedAt      time.Time `json:"checked_at"`
	PublishedAt    time.Time `json:"published_at,omitempty"`
	RunURL         string    `json:"run_url,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type githubWorkflowRunsResponse struct {
	WorkflowRuns []struct {
		HeadSHA   string    `json:"head_sha"`
		HTMLURL   string    `json:"html_url"`
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"workflow_runs"`
}

type VersionStatusService struct {
	mu          sync.Mutex
	client      *http.Client
	workflowURL string
	cacheTTL    time.Duration
	cached      VersionStatus
	cachedAt    time.Time
}

func NewVersionStatusService() *VersionStatusService {
	repository := strings.TrimSpace(os.Getenv("VERSION_CHECK_REPOSITORY"))
	if repository == "" {
		repository = defaultVersionRepository
	}
	branch := strings.TrimSpace(os.Getenv("VERSION_CHECK_BRANCH"))
	if branch == "" {
		branch = defaultVersionBranch
	}
	workflow := strings.TrimSpace(os.Getenv("VERSION_CHECK_WORKFLOW"))
	if workflow == "" {
		workflow = defaultVersionWorkflow
	}

	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		repository = defaultVersionRepository
		parts = strings.Split(repository, "/")
	}
	workflowURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/actions/workflows/%s/runs?branch=%s&status=success&per_page=1",
		url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(workflow), url.QueryEscape(branch),
	)
	return &VersionStatusService{
		client:      &http.Client{Timeout: 6 * time.Second},
		workflowURL: workflowURL,
		cacheTTL:    5 * time.Minute,
	}
}

func (s *VersionStatusService) Get(ctx context.Context, force bool) VersionStatus {
	if s == nil {
		return VersionStatus{CurrentVersion: CurrentBuildVersion(), Status: "unknown", CheckedAt: time.Now().UTC(), Error: "version checker unavailable"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && !s.cachedAt.IsZero() && time.Since(s.cachedAt) < s.cacheTTL {
		return s.cached
	}

	status := VersionStatus{
		CurrentVersion: CurrentBuildVersion(),
		Status:         "unknown",
		CheckedAt:      time.Now().UTC(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.workflowURL, nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "notion-manager-version-check")
		var resp *http.Response
		resp, err = s.client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
			} else {
				var runs githubWorkflowRunsResponse
				if decodeErr := json.NewDecoder(resp.Body).Decode(&runs); decodeErr != nil {
					err = decodeErr
				} else if len(runs.WorkflowRuns) == 0 || strings.TrimSpace(runs.WorkflowRuns[0].HeadSHA) == "" {
					err = fmt.Errorf("no successful Docker workflow run found")
				} else {
					latest := runs.WorkflowRuns[0]
					status.LatestVersion = strings.TrimSpace(latest.HeadSHA)
					status.PublishedAt = latest.UpdatedAt
					status.RunURL = latest.HTMLURL
					if versionsMatch(status.CurrentVersion, status.LatestVersion) {
						status.Status = "up_to_date"
					} else if isCommitVersion(status.CurrentVersion) {
						status.Status = "update_available"
					}
				}
			}
		}
	}
	if err != nil {
		status.Error = err.Error()
	}
	s.cached = status
	s.cachedAt = time.Now()
	return status
}

func versionsMatch(current, latest string) bool {
	current = strings.ToLower(strings.TrimSpace(current))
	latest = strings.ToLower(strings.TrimSpace(latest))
	if current == "" || latest == "" {
		return false
	}
	return current == latest || (len(current) >= 7 && strings.HasPrefix(latest, current)) || (len(latest) >= 7 && strings.HasPrefix(current, latest))
}

func isCommitVersion(version string) bool {
	version = strings.TrimSpace(version)
	if len(version) < 7 || len(version) > 64 {
		return false
	}
	for _, r := range version {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func HandleAdminVersionStatus(service *VersionStatusService, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if auth != nil && auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		force := r.URL.Query().Get("refresh") == "1"
		_ = json.NewEncoder(w).Encode(service.Get(r.Context(), force))
	}
}
