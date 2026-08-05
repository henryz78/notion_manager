package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"notion-manager/internal/accountstore"
)

// AccountDTO is the safe admin/dashboard representation.
// Never contains token_v2, cookies, or authorization headers.
type AccountDTO struct {
	AccountID    string     `json:"account_id"`
	UserEmail    string     `json:"user_email"`
	UserName     string     `json:"user_name"`
	SpaceName    string     `json:"space_name"`
	SpaceIDShort string     `json:"space_id_short"`
	PlanType     string     `json:"plan_type"`
	QuotaInfo    *QuotaInfo `json:"quota_info,omitempty"`
	IsExhausted  bool       `json:"is_exhausted"`
	IsPermanent  bool       `json:"is_permanently_exhausted"`
	SpaceCount   int        `json:"space_count"`
}

// NewAccountDTO builds a safe DTO from an Account (no secrets).
func NewAccountDTO(acc *Account) AccountDTO {
	acc.EnsureAccountID()
	quota := acc.quotaSnapshot()
	return AccountDTO{
		AccountID:    acc.AccountID,
		UserEmail:    acc.UserEmail,
		UserName:     acc.UserName,
		SpaceName:    acc.SpaceName,
		SpaceIDShort: acc.ShortSpaceID(),
		PlanType:     acc.PlanType,
		QuotaInfo:    quota.Info,
		IsExhausted:  quota.ExhaustedAt != nil,
		IsPermanent:  quota.PermanentlyExhausted,
		SpaceCount:   acc.SpaceCount,
	}
}

// DiscoverAccountFromToken calls Notion APIs using the given token_v2 to discover
// all account information (user, space, models, quota).
func DiscoverAccountFromToken(tokenV2 string) (*Account, error) {
	return DiscoverAccountFromTokenWithOptions(tokenV2, AccountDiscoveryOptions{})
}

// AccountDiscoveryOptions pins a multi-account Notion session to the intended
// active user and workspace. Empty fields retain the legacy automatic choice.
type AccountDiscoveryOptions struct {
	ActiveUserID  string
	SpaceID       string
	ExpectedEmail string
}

var (
	discoveryHTTPClient  = getChromeHTTPClient
	discoveryFetchModels = FetchModels
	discoveryCheckQuota  = CheckQuota
)

// DiscoverAccountsFromToken returns one independently addressable profile for
// every workspace visible to the login. Paid/AI-enabled workspaces sort first.
func DiscoverAccountsFromToken(tokenV2 string) ([]*Account, error) {
	return discoverAccountsFromTokenWithOptions(
		tokenV2,
		AccountDiscoveryOptions{},
		getChromeHTTPClient,
		modelsFetcher,
		quotaFetcher,
	)
}

func discoverAccountsFromTokenWithOptions(
	tokenV2 string,
	options AccountDiscoveryOptions,
	clientFactory func(time.Duration) *http.Client,
	fetchModels func(*Account) ([]ModelEntry, error),
	fetchQuota func(*Account) (*QuotaInfo, error),
) ([]*Account, error) {
	client := clientFactory(AppConfig.APITimeoutDuration())

	// Step 1: Call loadUserContent to get user/space info
	req, err := http.NewRequest("POST", NotionAPIBase+"/loadUserContent", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create loadUserContent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", AppConfig.Browser.UserAgent)
	activeUserID := strings.TrimSpace(options.ActiveUserID)
	cookieHeader := "token_v2=" + tokenV2
	if activeUserID != "" {
		cookieHeader = accountCookieHeader(&Account{TokenV2: tokenV2, UserID: activeUserID})
		req.Header.Set("x-notion-active-user-header", activeUserID)
	}
	req.Header.Set("Cookie", cookieHeader)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loadUserContent request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("loadUserContent API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	// Parse the response
	var userData struct {
		RecordMap struct {
			NotionUser  map[string]json.RawMessage `json:"notion_user"`
			UserRoot    map[string]json.RawMessage `json:"user_root"`
			Space       map[string]json.RawMessage `json:"space"`
			UserSetting map[string]json.RawMessage `json:"user_settings"`
		} `json:"recordMap"`
	}
	if err := json.Unmarshal(body, &userData); err != nil {
		return nil, fmt.Errorf("parse loadUserContent: %w", err)
	}

	type spaceViewPointer struct {
		SpaceID string `json:"spaceId"`
		ID      string `json:"id"`
	}
	spaceViewPointersForUser := func(userID string) []spaceViewPointer {
		var pointers []spaceViewPointer
		raw, ok := userData.RecordMap.UserRoot[userID]
		if !ok {
			return pointers
		}
		var ur struct {
			Value struct {
				Value *struct {
					SpaceViewPointers []spaceViewPointer `json:"space_view_pointers"`
				} `json:"value"`
				SpaceViewPointers []spaceViewPointer `json:"space_view_pointers"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &ur); err != nil {
			return pointers
		}
		if ur.Value.Value != nil {
			return ur.Value.Value.SpaceViewPointers
		}
		return ur.Value.SpaceViewPointers
	}

	// Extract user ID and info
	expectedEmail := strings.TrimSpace(options.ExpectedEmail)
	requestedSpaceID := strings.TrimSpace(options.SpaceID)
	var userID, userName, userEmail string
	for id, raw := range userData.RecordMap.NotionUser {
		if activeUserID != "" && id != activeUserID {
			continue
		}
		var u struct {
			Value struct {
				Value *struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"value"`
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			continue
		}
		candidateName := u.Value.Name
		candidateEmail := u.Value.Email
		if u.Value.Value != nil {
			candidateName = u.Value.Value.Name
			candidateEmail = u.Value.Value.Email
		}
		if expectedEmail != "" && !strings.EqualFold(candidateEmail, expectedEmail) {
			continue
		}
		if requestedSpaceID != "" {
			matchedSpace := false
			for _, ptr := range spaceViewPointersForUser(id) {
				if ptr.SpaceID == requestedSpaceID {
					matchedSpace = true
					break
				}
			}
			if !matchedSpace {
				continue
			}
		}
		userID = id
		userName = candidateName
		userEmail = candidateEmail
		break
	}
	if userID == "" {
		if activeUserID != "" || expectedEmail != "" || requestedSpaceID != "" {
			return nil, fmt.Errorf("no user matched the configured Notion account selectors")
		}
		return nil, fmt.Errorf("no user found in loadUserContent response")
	}

	// Extract space view pointers from user_root
	spaceViewPointers := spaceViewPointersForUser(userID)

	// Collect every accessible workspace. They are sorted by routing
	// preference so the backward-compatible single-account wrapper returns the
	// same paid/AI-enabled choice as the router.
	type spaceInfo struct {
		ID          string
		Name        string
		PlanType    string
		SpaceViewID string
		AIEnabled   bool
	}
	spaces := make([]spaceInfo, 0, len(spaceViewPointers))
	seenSpaceIDs := make(map[string]struct{}, len(spaceViewPointers))
	for _, ptr := range spaceViewPointers {
		if requestedSpaceID != "" && ptr.SpaceID != requestedSpaceID {
			continue
		}
		raw, ok := userData.RecordMap.Space[ptr.SpaceID]
		if !ok {
			continue
		}
		var s struct {
			Value struct {
				Value *struct {
					ID       string `json:"id"`
					Name     string `json:"name"`
					PlanType string `json:"plan_type"`
					Settings struct {
						EnableAIFeature  *bool `json:"enable_ai_feature"`
						DisableAIFeature *bool `json:"disable_ai_feature"`
					} `json:"settings"`
				} `json:"value"`
				ID       string `json:"id"`
				Name     string `json:"name"`
				PlanType string `json:"plan_type"`
				Settings struct {
					EnableAIFeature  *bool `json:"enable_ai_feature"`
					DisableAIFeature *bool `json:"disable_ai_feature"`
				} `json:"settings"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		var si spaceInfo
		si.SpaceViewID = ptr.ID
		if s.Value.Value != nil {
			si.ID = s.Value.Value.ID
			si.Name = s.Value.Value.Name
			si.PlanType = s.Value.Value.PlanType
			aiOff := (s.Value.Value.Settings.EnableAIFeature != nil && !*s.Value.Value.Settings.EnableAIFeature) ||
				(s.Value.Value.Settings.DisableAIFeature != nil && *s.Value.Value.Settings.DisableAIFeature)
			si.AIEnabled = !aiOff
		} else {
			si.ID = s.Value.ID
			si.Name = s.Value.Name
			si.PlanType = s.Value.PlanType
			aiOff := (s.Value.Settings.EnableAIFeature != nil && !*s.Value.Settings.EnableAIFeature) ||
				(s.Value.Settings.DisableAIFeature != nil && *s.Value.Settings.DisableAIFeature)
			si.AIEnabled = !aiOff
		}
		if si.ID == "" {
			si.ID = ptr.SpaceID
		}
		if _, duplicate := seenSpaceIDs[si.ID]; duplicate {
			continue
		}
		seenSpaceIDs[si.ID] = struct{}{}
		spaces = append(spaces, si)
	}
	if len(spaces) == 0 {
		if requestedSpaceID != "" {
			return nil, fmt.Errorf("requested NOTION_SPACE_ID was not found for the active account")
		}
		return nil, fmt.Errorf("no workspace found for this account")
	}
	sort.SliceStable(spaces, func(i, j int) bool {
		left := workspacePreference(notionWorkspaceMetadata{
			ID: spaces[i].ID, Name: spaces[i].Name,
			PlanType: spaces[i].PlanType, AIEnabled: spaces[i].AIEnabled,
		})
		right := workspacePreference(notionWorkspaceMetadata{
			ID: spaces[j].ID, Name: spaces[j].Name,
			PlanType: spaces[j].PlanType, AIEnabled: spaces[j].AIEnabled,
		})
		return left > right
	})

	// Extract timezone from user_settings
	timezone := "UTC"
	if raw, ok := userData.RecordMap.UserSetting[userID]; ok {
		var us struct {
			Value struct {
				Value *struct {
					Settings struct {
						TimeZone string `json:"time_zone"`
					} `json:"settings"`
				} `json:"value"`
				Settings struct {
					TimeZone string `json:"time_zone"`
				} `json:"settings"`
			} `json:"value"`
		}
		if err := json.Unmarshal(raw, &us); err == nil {
			if us.Value.Value != nil && us.Value.Value.Settings.TimeZone != "" {
				timezone = us.Value.Value.Settings.TimeZone
			} else if us.Value.Settings.TimeZone != "" {
				timezone = us.Value.Settings.TimeZone
			}
		}
	}

	workspaceCheckedAt := time.Now()
	accounts := make([]*Account, 0, len(spaces))
	for _, space := range spaces {
		workspaceAIEnabled := space.AIEnabled
		acc := &Account{
			TokenV2:            tokenV2,
			UserID:             userID,
			UserName:           userName,
			UserEmail:          userEmail,
			SpaceID:            space.ID,
			SpaceName:          space.Name,
			SpaceViewID:        space.SpaceViewID,
			PlanType:           space.PlanType,
			Timezone:           timezone,
			ClientVersion:      DefaultClientVersion,
			BrowserID:          generateUUIDv4(),
			DeviceID:           generateUUIDv4(),
			SpaceCount:         len(spaces),
			WorkspaceCheckedAt: &workspaceCheckedAt,
			WorkspaceAIEnabled: &workspaceAIEnabled,
		}
		acc.EnsureAccountID()

		// Models and quota can differ by workspace even under one login.
		models, err := fetchModels(acc)
		if err != nil {
			log.Printf("[add-account] model fetch failed for workspace=%s (non-fatal): %v", acc.ShortSpaceID(), err)
		} else {
			acc.setModels(models)
		}
		quota, err := fetchQuota(acc)
		if err != nil {
			log.Printf("[add-account] quota check failed for workspace=%s (non-fatal): %v", acc.ShortSpaceID(), err)
		} else {
			now := time.Now()
			acc.setQuotaInfo(quota, &now)
		}
		accounts = append(accounts, acc)
	}
	return accounts, nil
}

// DiscoverAccountFromTokenWithOptions selects one exact login/workspace from a
// multi-account browser session while retaining the multi-workspace discovery
// implementation used by the dashboard.
func DiscoverAccountFromTokenWithOptions(tokenV2 string, options AccountDiscoveryOptions) (*Account, error) {
	accounts, err := discoverAccountsFromTokenWithOptions(
		tokenV2,
		options,
		discoveryHTTPClient,
		discoveryFetchModels,
		discoveryCheckQuota,
	)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no workspace found for this account")
	}
	return accounts[0], nil
}

var discoverAccountsFromToken = DiscoverAccountsFromToken

// SaveAccountToFile writes an Account to a JSON file in the accounts directory.
// The filename uses the full account_id for collision safety:
//
//	<account_id>__<sanitized_email>.json
//
// If a file with the same account_id already exists (possibly under an old
// naming scheme), it is overwritten at the same path. This prevents
// duplicate files after migration.
func SaveAccountToFile(acc *Account, dir string) (string, error) {
	acc.EnsureAccountID()
	if err := ensurePrivateAccountsDir(dir); err != nil {
		return "", fmt.Errorf("create accounts dir: %w", err)
	}
	// Secure every credential before accountstore scans it for a matching
	// workspace identity. accountstore repeats the protection under its own
	// directory lock, closing the race with concurrent file creation.
	entries, err := readPrivateAccountsDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		if _, err := readPrivateAccountFile(filepath.Join(dir, entry.Name())); err != nil {
			return "", err
		}
	}

	name := acc.UserEmail
	if name == "" {
		name = acc.UserName
	}
	if name == "" {
		name = "unknown"
	}
	// Build the JSON structure
	acc.EnsureAccountID()
	data := map[string]interface{}{
		"token_v2":       acc.TokenV2,
		"user_id":        acc.UserID,
		"user_name":      acc.UserName,
		"user_email":     acc.UserEmail,
		"space_id":       acc.SpaceID,
		"space_name":     acc.SpaceName,
		"space_view_id":  acc.SpaceViewID,
		"plan_type":      acc.PlanType,
		"timezone":       acc.Timezone,
		"client_version": acc.ClientVersion,
		"browser_id":     acc.BrowserID,
		"device_id":      acc.DeviceID,
	}
	if acc.AccountID != "" {
		data["account_id"] = acc.AccountID
	}
	profile := acc.profileSnapshot()
	if profile.WorkspaceCheckedAt != nil {
		data["space_count"] = profile.SpaceCount
		data["workspace_checked_at"] = profile.WorkspaceCheckedAt.Format(time.RFC3339)
		if profile.AIEnabled != nil {
			data["workspace_ai_enabled"] = *profile.AIEnabled
		}
	}
	modelSnapshot := acc.modelsSnapshot()
	quota := acc.quotaSnapshot()
	if len(modelSnapshot) > 0 {
		var models []map[string]string
		for _, m := range modelSnapshot {
			models = append(models, map[string]string{"id": m.ID, "name": m.Name})
		}
		data["available_models"] = models
	}
	if quota.Info != nil {
		data["quota_info"] = map[string]interface{}{
			"is_eligible":         quota.Info.IsEligible,
			"space_usage":         quota.Info.SpaceUsage,
			"space_limit":         quota.Info.SpaceLimit,
			"user_usage":          quota.Info.UserUsage,
			"user_limit":          quota.Info.UserLimit,
			"last_usage_at":       quota.Info.LastUsageAtMs,
			"research_mode_usage": quota.Info.ResearchModeUsage,
			"has_premium":         quota.Info.HasPremium,
			"premium_balance":     quota.Info.PremiumBalance,
			"premium_usage":       quota.Info.PremiumUsage,
			"premium_limit":       quota.Info.PremiumLimit,
		}
	}
	if quota.CheckedAt != nil {
		data["quota_checked_at"] = quota.CheckedAt.Format(time.RFC3339)
	}
	writePersonalInstructionsState(data, acc.personalInstructionsSnapshot())
	data["extracted_at"] = time.Now().Format(time.RFC3339)

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal account JSON: %w", err)
	}

	path, err := accountstore.WriteAccountJSON(dir, acc.AccountID, name, out)
	if err != nil {
		return "", err
	}
	return filepath.Base(path), nil
}

// AddAccount adds an account to the pool (hot-load, no restart needed).
func (p *AccountPool) AddAccount(acc *Account) {
	if p == nil || acc == nil {
		return
	}
	var replaced *Account
	p.mu.Lock()
	// Check for duplicate by account_id (same user_id + space_id)
	acc.EnsureAccountID()
	for i, existing := range p.accounts {
		existing.EnsureAccountID()
		if existing.AccountID != "" && existing.AccountID == acc.AccountID {
			// Replace existing (same workspace)
			replaced = existing
			p.accounts[i] = acc
			break
		}
	}
	if replaced == nil {
		p.accounts = append(p.accounts, acc)
	}
	remaining := append([]*Account(nil), p.accounts...)
	p.mu.Unlock()

	if replaced != nil {
		p.liveQuotaMu.Lock()
		delete(p.liveQuotaFlights, replaced)
		delete(p.quotaGeneration, replaced)
		delete(p.quotaApplied, replaced)
		p.liveQuotaMu.Unlock()
		globalSessionManager.DeleteByAccountID(replaced.AccountID, replaced.UserEmail)
		log.Printf("[account] replaced: %s (%s) aid=%s", acc.UserName, acc.UserEmail, acc.ShortSpaceID())
	} else {
		log.Printf("[account] added: %s (%s) [%s]", acc.UserName, acc.UserEmail, acc.PlanType)
	}
	rebuildDynamicModelMap(remaining)
}

// DeleteAccountFile removes the JSON file for an account from the accounts directory.
// Matches by account_id first; falls back to user_id+space_id.
func DeleteAccountFile(accountID, dir string) error {
	return deleteAccountFileByIdentity(accountID, "", dir)
}

func deleteAccountFileByIdentity(accountID, expectedToken, dir string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("account_id is required")
	}
	unlockDirectory, err := accountstore.LockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlockDirectory()
	return deleteAccountFileByIdentityLocked(accountID, expectedToken, dir)
}

func deleteAccountFileByIdentityLocked(accountID, expectedToken, dir string) error {
	entries, err := readPrivateAccountsDir(dir)
	if err != nil {
		return err
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := readPrivateAccountFile(path)
		if err != nil {
			return err
		}
		var existing map[string]interface{}
		if err := json.Unmarshal(data, &existing); err != nil {
			continue
		}
		if accountIDFromRaw(existing) == accountID {
			matches = append(matches, path)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("account file not found for account_id %s: %w", accountID, os.ErrNotExist)
	case 1:
	default:
		return fmt.Errorf("multiple account files match account_id %s; refusing partial deletion", accountID)
	}
	path := matches[0]
	unlockFile, err := lockAccountFilePath(path)
	if err != nil {
		return err
	}
	defer unlockFile()
	latest, err := readPrivateAccountFile(path)
	if err != nil {
		return fmt.Errorf("re-read account file %s: %w", filepath.Base(path), err)
	}
	var latestAccount map[string]interface{}
	if err := json.Unmarshal(latest, &latestAccount); err != nil {
		return fmt.Errorf("parse account file %s: %w", filepath.Base(path), err)
	}
	if accountIDFromRaw(latestAccount) != accountID {
		return fmt.Errorf("account identity changed during deletion; replacement retained")
	}
	if expectedToken != "" {
		if token, _ := latestAccount["token_v2"].(string); token != expectedToken {
			return fmt.Errorf("account identity changed during deletion; replacement retained")
		}
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete file %s: %w", filepath.Base(path), err)
	}
	log.Printf("[account] deleted file: %s", filepath.Base(path))
	return nil
}

// DeleteAccountFileByEmail removes the JSON file by email (legacy).
// Returns AmbiguousEmailError if multiple files match.
func DeleteAccountFileByEmail(email, dir string) error {
	return deleteAccountFileByEmailIdentity(email, "", dir)
}

func deleteAccountFileByEmailIdentity(email, expectedToken, dir string) error {
	unlockDirectory, err := accountstore.LockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlockDirectory()
	return deleteAccountFileByEmailIdentityLocked(email, expectedToken, dir)
}

func deleteAccountFileByEmailIdentityLocked(email, expectedToken, dir string) error {
	entries, err := readPrivateAccountsDir(dir)
	if err != nil {
		return fmt.Errorf("read accounts dir: %w", err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := readPrivateAccountFile(path)
		if err != nil {
			return err
		}
		var existing map[string]interface{}
		if err := json.Unmarshal(data, &existing); err != nil {
			continue
		}
		e, _ := existing["user_email"].(string)
		token, _ := existing["token_v2"].(string)
		if strings.EqualFold(e, email) && (expectedToken == "" || token == expectedToken) {
			matches = append(matches, path)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("account file not found for %s: %w", email, os.ErrNotExist)
	case 1:
		target := matches[0]
		unlockFile, err := lockAccountFilePath(target)
		if err != nil {
			return err
		}
		defer unlockFile()
		latest, err := readPrivateAccountFile(target)
		if err != nil {
			return err
		}
		var latestAccount map[string]interface{}
		if err := json.Unmarshal(latest, &latestAccount); err != nil {
			return err
		}
		if latestEmail, _ := latestAccount["user_email"].(string); !strings.EqualFold(latestEmail, email) {
			return fmt.Errorf("account identity changed during deletion; replacement retained")
		}
		if expectedToken != "" {
			if latestToken, _ := latestAccount["token_v2"].(string); latestToken != expectedToken {
				return fmt.Errorf("account identity changed during deletion; replacement retained")
			}
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
		log.Printf("[account] deleted file: %s", filepath.Base(target))
		return nil
	default:
		return &AmbiguousEmailError{Email: email, Count: len(matches)}
	}
}

// HandleAddAccount accepts a token_v2, discovers account info via Notion APIs,
// saves it to disk, and hot-loads it into the pool.
func beginAccountMutationRequest(w http.ResponseWriter, pool *AccountPool) (func(), bool) {
	release, ok := pool.beginAccountMutation()
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, accountRestoreConflictMessage), http.StatusConflict)
		return nil, false
	}
	return release, true
}

func HandleAddAccount(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Require dashboard session
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		var body struct {
			TokenV2                    string `json:"token_v2"`
			PersonalInstructionsPolicy string `json:"personal_instructions_policy"`
			NotionUserID               string `json:"notion_user_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		tokenV2 := strings.TrimSpace(body.TokenV2)
		if tokenV2 == "" {
			http.Error(w, `{"error":"token_v2 is required"}`, http.StatusBadRequest)
			return
		}
		policySpecified := strings.TrimSpace(body.PersonalInstructionsPolicy) != ""
		policy := strings.ToLower(strings.TrimSpace(body.PersonalInstructionsPolicy))
		if policy == "" {
			policy = "all"
		}
		if policy != "all" && policy != "configured_only" {
			http.Error(w, `{"error":"personal_instructions_policy must be all or configured_only"}`, http.StatusBadRequest)
			return
		}
		log.Printf("[add-account] discovering account from token_v2 (%d chars)...", len(tokenV2))

		// Discover every workspace visible to this login. An already imported
		// token is still probed because a paid workspace may have been added
		// since the first import.
		var discovered []*Account
		var err error
		if activeUserID := strings.TrimSpace(body.NotionUserID); activeUserID != "" {
			discovered, err = discoverAccountsFromTokenWithOptions(
				tokenV2,
				AccountDiscoveryOptions{ActiveUserID: activeUserID},
				discoveryHTTPClient,
				discoveryFetchModels,
				discoveryCheckQuota,
			)
		} else {
			discovered, err = discoverAccountsFromToken(tokenV2)
		}
		if err != nil {
			log.Printf("[add-account] discovery failed: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("Failed to discover account: %v", err),
			})
			return
		}

		checkPersonalInstructions := policySpecified || policy == "configured_only"
		importedAccounts := make([]map[string]string, 0, len(discovered))
		filenames := make([]string, 0, len(discovered))
		skipped := 0
		for _, acc := range discovered {
			acc.EnsureAccountID()
			if existing := pool.FindByAccountID(acc.AccountID); existing != nil && existing.TokenV2 == tokenV2 {
				skipped++
				continue
			}

			if checkPersonalInstructions {
				configured, checkError := checkPersonalInstructionsForImport(acc, fetchNotionPersonalInstructionsPageID)
				if policy == "configured_only" {
					if checkError != "" {
						log.Printf("[add-account] personal-instructions check failed for %s workspace=%s: %s",
							acc.UserEmail, acc.ShortSpaceID(), checkError)
						skipped++
						continue
					}
					if configured == nil || !*configured {
						log.Printf("[add-account] skipped %s workspace=%s: default-Agent personal instructions not configured",
							acc.UserEmail, acc.ShortSpaceID())
						skipped++
						continue
					}
				}
			}

			filename, saveErr := SaveAccountToFile(acc, accountsDir)
			if saveErr != nil {
				log.Printf("[add-account] save failed for workspace=%s: %v", acc.ShortSpaceID(), saveErr)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error":    fmt.Sprintf("Failed to save account: %v", saveErr),
					"imported": len(importedAccounts),
				})
				return
			}
			if activateErr := pool.ActivateAccountByIDFromDir(accountsDir, acc.AccountID); activateErr != nil {
				log.Printf("[add-account] activate failed for workspace=%s: %v", acc.ShortSpaceID(), activateErr)
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error":    fmt.Sprintf("Failed to activate account: %v", activateErr),
					"imported": len(importedAccounts),
				})
				return
			}
			activeAccount := pool.FindByAccountID(acc.AccountID)
			if activeAccount == nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error":    "Account changed while it was being activated",
					"imported": len(importedAccounts),
				})
				return
			}
			filenames = append(filenames, filename)
			importedAccounts = append(importedAccounts, accountImportInfo(activeAccount))
			log.Printf("[add-account] success: %s (%s) workspace=%s → %s",
				activeAccount.UserName, activeAccount.UserEmail, activeAccount.ShortSpaceID(), filename)
		}

		if len(importedAccounts) == 0 {
			reason := "duplicate_account"
			if policy == "configured_only" && skipped > 0 {
				reason = "personal_instructions_missing"
			}
			response := map[string]interface{}{
				"status":  "skipped",
				"reason":  reason,
				"skipped": skipped,
			}
			if len(discovered) > 0 {
				response["account"] = accountImportInfo(discovered[0])
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		}

		response := map[string]interface{}{
			"status":    "ok",
			"filename":  filenames[0],
			"filenames": filenames,
			"account":   importedAccounts[0],
			"accounts":  importedAccounts,
			"imported":  len(importedAccounts),
			"skipped":   skipped,
		}
		if checkPersonalInstructions {
			response["personal_instructions_checked"] = true
		}
		_ = json.NewEncoder(w).Encode(response)
	}
}

func accountImportInfo(acc *Account) map[string]string {
	if acc == nil {
		return map[string]string{}
	}
	acc.EnsureAccountID()
	planType := acc.planTypeSnapshot()
	return map[string]string{
		"account_id":     acc.AccountID,
		"name":           acc.UserName,
		"email":          acc.UserEmail,
		"space":          acc.SpaceName,
		"space_id_short": acc.ShortSpaceID(),
		"plan_type":      planType,
	}
}

type PersonalInstructionsCheckItem struct {
	AccountID  string `json:"account_id,omitempty"`
	Email      string `json:"email"`
	Configured *bool  `json:"configured,omitempty"`
	CheckedAt  string `json:"checked_at"`
	Error      string `json:"error,omitempty"`
}

type PersonalInstructionsCheckSummary struct {
	Status     string                          `json:"status"`
	Total      int                             `json:"total"`
	Configured int                             `json:"configured"`
	Missing    int                             `json:"missing"`
	Failed     int                             `json:"failed"`
	Results    []PersonalInstructionsCheckItem `json:"results"`
}

type personalInstructionsPageIDFetcher func(*Account) (string, error)

func checkPersonalInstructionsForImport(acc *Account, fetcher personalInstructionsPageIDFetcher) (*bool, string) {
	if acc == nil || fetcher == nil {
		return nil, ""
	}
	checkedAt := time.Now().UTC()
	pageID, err := fetcher(acc)
	if err != nil {
		checkError := truncateForLog(err.Error(), 300)
		acc.setPersonalInstructionsCheck(nil, checkedAt, checkError)
		return nil, checkError
	}
	configured := strings.TrimSpace(pageID) != ""
	acc.setPersonalInstructionsCheck(&configured, checkedAt, "")
	return &configured, ""
}

func checkAllPersonalInstructions(pool *AccountPool, concurrency int, fetcher personalInstructionsPageIDFetcher) PersonalInstructionsCheckSummary {
	if pool == nil {
		return PersonalInstructionsCheckSummary{Status: "ok"}
	}
	pool.mu.RLock()
	accounts := append([]*Account(nil), pool.accounts...)
	pool.mu.RUnlock()
	return checkPersonalInstructionsForAccounts(accounts, concurrency, fetcher)
}

func checkPersonalInstructionsForAccounts(accounts []*Account, concurrency int, fetcher personalInstructionsPageIDFetcher) PersonalInstructionsCheckSummary {
	summary := PersonalInstructionsCheckSummary{Status: "ok"}
	if fetcher == nil {
		return summary
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 20 {
		concurrency = 20
	}

	summary.Total = len(accounts)
	if len(accounts) == 0 {
		return summary
	}

	jobs := make(chan *Account)
	results := make(chan PersonalInstructionsCheckItem, len(accounts))
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for acc := range jobs {
				checkedAt := time.Now().UTC()
				acc.EnsureAccountID()
				item := PersonalInstructionsCheckItem{
					AccountID: acc.AccountID,
					Email:     acc.UserEmail,
					CheckedAt: checkedAt.Format(time.RFC3339),
				}
				pageID, err := fetcher(acc)
				if err != nil {
					item.Error = truncateForLog(err.Error(), 300)
					acc.setPersonalInstructionsCheck(nil, checkedAt, item.Error)
					results <- item
					continue
				}
				configured := strings.TrimSpace(pageID) != ""
				item.Configured = &configured
				acc.setPersonalInstructionsCheck(&configured, checkedAt, "")
				results <- item
			}
		}()
	}

	go func() {
		for _, acc := range accounts {
			jobs <- acc
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	for item := range results {
		switch {
		case item.Error != "":
			summary.Failed++
		case item.Configured != nil && *item.Configured:
			summary.Configured++
		default:
			summary.Missing++
		}
		summary.Results = append(summary.Results, item)
	}
	sort.Slice(summary.Results, func(i, j int) bool {
		return strings.ToLower(summary.Results[i].Email) < strings.ToLower(summary.Results[j].Email)
	})
	return summary
}

// HandleCheckPersonalInstructions checks only whether each account has a
// default-Agent personal-instructions page configured. It never loads, returns,
// logs, or persists the page ID or the page contents.
func HandleCheckPersonalInstructions(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		summary := checkAllPersonalInstructions(pool, 10, fetchNotionPersonalInstructionsPageID)
		if accountsDir != "" {
			pool.SaveAccounts(accountsDir)
		}
		log.Printf("[personal-instructions-check] total=%d configured=%d missing=%d failed=%d",
			summary.Total, summary.Configured, summary.Missing, summary.Failed)
		_ = json.NewEncoder(w).Encode(summary)
	}
}

const maxBulkAccountEmails = 5000

type BulkAccountActionRequest struct {
	Action     string   `json:"action"`
	AccountIDs []string `json:"account_ids,omitempty"`
	Emails     []string `json:"emails,omitempty"`
}

type BulkAccountActionResult struct {
	Status    string                            `json:"status"`
	Action    string                            `json:"action"`
	Requested int                               `json:"requested"`
	Matched   int                               `json:"matched"`
	Succeeded int                               `json:"succeeded"`
	Failed    map[string]string                 `json:"failed"`
	Check     *PersonalInstructionsCheckSummary `json:"check,omitempty"`
}

func normalizeBulkEmails(emails []string) []string {
	normalized := make([]string, 0, len(emails))
	seen := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		key := strings.ToLower(email)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, email)
	}
	return normalized
}

func accountsMatchingEmailsDetailed(pool *AccountPool, requested []string) ([]*Account, map[string]string) {
	failures := make(map[string]string)
	if pool == nil || len(requested) == 0 {
		for _, email := range requested {
			failures[email] = "account not found"
		}
		return nil, failures
	}
	pool.mu.RLock()
	byEmail := make(map[string][]*Account, len(pool.accounts))
	for _, acc := range pool.accounts {
		key := strings.ToLower(strings.TrimSpace(acc.UserEmail))
		if key != "" {
			byEmail[key] = append(byEmail[key], acc)
		}
	}
	pool.mu.RUnlock()

	matched := make([]*Account, 0, len(requested))
	for _, email := range requested {
		switch matches := byEmail[strings.ToLower(email)]; len(matches) {
		case 0:
			failures[email] = "account not found"
		case 1:
			matched = append(matched, matches[0])
		default:
			failures[email] = "email matches multiple workspaces; use account_id"
		}
	}
	return matched, failures
}

func accountsMatchingEmails(pool *AccountPool, requested []string) ([]*Account, []string) {
	matched, failures := accountsMatchingEmailsDetailed(pool, requested)
	missing := make([]string, 0, len(failures))
	for _, email := range requested {
		if _, failed := failures[email]; failed {
			missing = append(missing, email)
		}
	}
	return matched, missing
}

func normalizeAccountIDs(accountIDs []string) []string {
	normalized := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.ToLower(strings.TrimSpace(accountID))
		if !isAccountID(accountID) {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		normalized = append(normalized, accountID)
	}
	return normalized
}

func accountsMatchingAccountIDs(pool *AccountPool, requested []string) ([]*Account, []string) {
	if pool == nil || len(requested) == 0 {
		return nil, requested
	}
	matched := make([]*Account, 0, len(requested))
	missing := make([]string, 0)
	for _, accountID := range requested {
		if acc := pool.FindByAccountID(accountID); acc != nil {
			matched = append(matched, acc)
		} else {
			missing = append(missing, accountID)
		}
	}
	return matched, missing
}

type parallelAccountResult struct {
	accountID string
	email     string
	err       error
}

func runAccountsParallel(accounts []*Account, concurrency int, worker func(*Account) error) []parallelAccountResult {
	if len(accounts) == 0 || worker == nil {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 20 {
		concurrency = 20
	}
	jobs := make(chan *Account)
	results := make(chan parallelAccountResult, len(accounts))
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for account := range jobs {
				account.EnsureAccountID()
				results <- parallelAccountResult{
					accountID: account.AccountID,
					email:     account.UserEmail,
					err:       worker(account),
				}
			}
		}()
	}
	go func() {
		for _, account := range accounts {
			jobs <- account
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	out := make([]parallelAccountResult, 0, len(accounts))
	for result := range results {
		out = append(out, result)
	}
	return out
}

// HandleBulkAccountAction applies an operator action to explicitly selected
// accounts. Manual disable is persisted and only changes routing eligibility;
// it does not overwrite quota, auth, or personal-instructions state.
func HandleBulkAccountAction(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		var body BulkAccountActionRequest
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		body.Action = strings.ToLower(strings.TrimSpace(body.Action))
		accountIDs := normalizeAccountIDs(body.AccountIDs)
		emails := normalizeBulkEmails(body.Emails)
		if len(accountIDs) == 0 && len(emails) == 0 {
			http.Error(w, `{"error":"at least one account is required"}`, http.StatusBadRequest)
			return
		}
		if len(accountIDs) > maxBulkAccountEmails || len(emails) > maxBulkAccountEmails {
			http.Error(w, `{"error":"too many accounts selected"}`, http.StatusBadRequest)
			return
		}
		switch body.Action {
		case "delete", "disable", "enable", "check_personal_instructions":
		default:
			http.Error(w, `{"error":"unsupported bulk action"}`, http.StatusBadRequest)
			return
		}

		accounts := make([]*Account, 0)
		missing := make([]string, 0)
		emailFailures := make(map[string]string)
		requested := emails
		if len(accountIDs) > 0 {
			accounts, missing = accountsMatchingAccountIDs(pool, accountIDs)
			requested = accountIDs
		} else {
			accounts, emailFailures = accountsMatchingEmailsDetailed(pool, emails)
		}
		result := BulkAccountActionResult{
			Status:    "ok",
			Action:    body.Action,
			Requested: len(requested),
			Matched:   len(accounts),
			Failed:    make(map[string]string),
		}
		if len(accountIDs) == 0 {
			for email, message := range emailFailures {
				result.Failed[email] = message
			}
		} else {
			for _, accountID := range missing {
				result.Failed[accountID] = "account not found"
			}
		}

		switch body.Action {
		case "delete":
			for _, item := range runAccountsParallel(accounts, 10, func(acc *Account) error {
				acc.EnsureAccountID()
				if acc.AccountID == "" {
					return deleteAccountByIdentity(pool, accountsDir, acc.UserEmail, acc.TokenV2)
				}
				return deleteAccountByAccountIdentity(pool, accountsDir, acc.AccountID, acc.UserEmail, acc.TokenV2)
			}) {
				resultKey := item.accountID
				if len(accountIDs) == 0 {
					resultKey = item.email
				}
				if item.err != nil {
					result.Failed[resultKey] = item.err.Error()
					continue
				}
				result.Succeeded++
			}
		case "disable", "enable":
			disabled := body.Action == "disable"
			for _, item := range runAccountsParallel(accounts, 10, func(acc *Account) error {
				previous := acc.setManuallyDisabled(disabled)
				if err := saveAccountFile(accountsDir, acc); err != nil {
					acc.setManuallyDisabled(previous)
					return err
				}
				return nil
			}) {
				resultKey := item.accountID
				if len(accountIDs) == 0 {
					resultKey = item.email
				}
				if item.err != nil {
					result.Failed[resultKey] = item.err.Error()
					continue
				}
				result.Succeeded++
			}
		case "check_personal_instructions":
			check := checkPersonalInstructionsForAccounts(accounts, 10, fetchNotionPersonalInstructionsPageID)
			result.Check = &check
			result.Succeeded = check.Total - check.Failed
			for _, item := range check.Results {
				if item.Error != "" {
					result.Failed[item.Email] = item.Error
				}
			}
			if accountsDir != "" {
				pool.SaveAccounts(accountsDir)
			}
		}

		log.Printf("[bulk-account-action] action=%s requested=%d matched=%d succeeded=%d failed=%d",
			result.Action, result.Requested, result.Matched, result.Succeeded, len(result.Failed))
		_ = json.NewEncoder(w).Encode(result)
	}
}

type DeleteMissingPersonalInstructionsResult struct {
	Status  string                           `json:"status"`
	Checked int                              `json:"checked"`
	Matched int                              `json:"matched"`
	Deleted int                              `json:"deleted"`
	Emails  []string                         `json:"emails"`
	Failed  map[string]string                `json:"failed"`
	Check   PersonalInstructionsCheckSummary `json:"check"`
}

func deleteMissingPersonalInstructions(pool *AccountPool, accountsDir string, fetcher personalInstructionsPageIDFetcher) DeleteMissingPersonalInstructionsResult {
	check := checkAllPersonalInstructions(pool, 10, fetcher)
	if pool != nil && accountsDir != "" {
		pool.SaveAccounts(accountsDir)
	}

	candidateIDs := make([]string, 0, check.Missing)
	candidateEmails := make([]string, 0, check.Missing)
	for _, item := range check.Results {
		if item.Error == "" && item.Configured != nil && !*item.Configured {
			if item.AccountID != "" {
				candidateIDs = append(candidateIDs, item.AccountID)
			} else {
				candidateEmails = append(candidateEmails, item.Email)
			}
		}
	}
	candidateCount := len(candidateIDs) + len(candidateEmails)
	result := DeleteMissingPersonalInstructionsResult{
		Status:  "ok",
		Checked: check.Total,
		Matched: candidateCount,
		Emails:  make([]string, 0, candidateCount),
		Failed:  make(map[string]string),
		Check:   check,
	}
	accounts, missingIDs := accountsMatchingAccountIDs(pool, candidateIDs)
	legacyAccounts, legacyFailures := accountsMatchingEmailsDetailed(pool, candidateEmails)
	accounts = append(accounts, legacyAccounts...)
	for _, accountID := range missingIDs {
		result.Failed[accountID] = "account not found"
	}
	for email, message := range legacyFailures {
		result.Failed[email] = message
	}
	for _, item := range runAccountsParallel(accounts, 10, func(acc *Account) error {
		acc.EnsureAccountID()
		if acc.AccountID == "" {
			return deleteAccountByIdentity(pool, accountsDir, acc.UserEmail, acc.TokenV2)
		}
		return deleteAccountByAccountIdentity(pool, accountsDir, acc.AccountID, acc.UserEmail, acc.TokenV2)
	}) {
		if item.err != nil {
			result.Failed[item.email] = item.err.Error()
			continue
		}
		result.Emails = append(result.Emails, item.email)
		result.Deleted++
	}
	sort.Strings(result.Emails)
	return result
}

// HandleDeleteMissingPersonalInstructions re-checks the full pool and then
// permanently deletes only accounts that currently have no default-Agent
// personal-instructions page. Probe failures are retained.
func HandleDeleteMissingPersonalInstructions(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		result := deleteMissingPersonalInstructions(pool, accountsDir, fetchNotionPersonalInstructionsPageID)
		log.Printf("[delete-missing-personal-instructions] checked=%d missing=%d deleted=%d failed=%d",
			result.Checked, result.Matched, result.Deleted, len(result.Failed))
		_ = json.NewEncoder(w).Encode(result)
	}
}

// HandleDeleteAccount removes an account from the pool and deletes its file.
func HandleDeleteAccount(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != "POST" {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Require dashboard session
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		var body struct {
			AccountID string `json:"account_id"`
			Email     string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		accountID := strings.TrimSpace(body.AccountID)
		email := strings.TrimSpace(body.Email)
		if accountID == "" && email == "" {
			http.Error(w, `{"error":"account_id or email is required"}`, http.StatusBadRequest)
			return
		}

		var deleteErr error
		if accountID != "" {
			deleteErr = deleteAccountByID(pool, accountsDir, accountID)
		} else {
			deleteErr = deleteAccountByEmail(pool, accountsDir, email)
		}
		if deleteErr != nil {
			if os.IsNotExist(deleteErr) {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "account not found"})
				return
			}
			var ambiguous *AmbiguousEmailError
			if errors.As(deleteErr, &ambiguous) {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{"error": deleteErr.Error()})
				return
			}
			log.Printf("[delete-account] deletion failed: %v", deleteErr)
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": deleteErr.Error()})
			return
		}

		if accountID != "" {
			log.Printf("[delete-account] removed account_id=%s", accountID)
		} else {
			log.Printf("[delete-account] removed legacy email=%s", email)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// isComplimentaryPlanType reports plans that currently receive only a limited
// complimentary Notion AI trial. Included-AI paid plans are deliberately
// excluded.
func isComplimentaryPlanType(plan string) bool {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "free", "personal", "plus", "personal_pro":
		return true
	default:
		return false
	}
}

// isExhaustedComplimentaryAccount is intentionally narrower than the general
// unusable-account check: bulk cleanup must not delete accounts disabled only
// by an expired cookie, missing workspace, temporary upstream failure, or an
// included-AI paid-plan issue. Private Premium credit fields are not used as
// workspace-plan evidence.
func isExhaustedComplimentaryAccount(acc *Account) bool {
	if acc == nil || !isComplimentaryPlanType(acc.planTypeSnapshot()) {
		return false
	}
	health := acc.healthSnapshot()
	if health.AuthInvalid || (health.TemporaryUnavailableUntil != nil && health.TemporaryUnavailableUntil.After(time.Now())) {
		return false
	}
	profile := acc.profileSnapshot()
	if profile.WorkspaceCheckedAt != nil && profile.SpaceCount == 0 {
		return false
	}
	quota := acc.quotaSnapshot()
	if quota.PermanentlyExhausted || quota.ExhaustedAt != nil {
		return true
	}
	return quota.Info != nil && !quota.Info.IsEligible
}

func confirmExhaustedComplimentaryAccount(pool *AccountPool, acc *Account) (bool, error) {
	if pool == nil || acc == nil {
		return false, nil
	}
	workspace, err := workspaceProbe(acc)
	if err != nil {
		return false, fmt.Errorf("refresh workspace plan: %w", err)
	}
	pool.applyWorkspaceProfile(acc, workspace)
	if pool.hasNoWorkspace(acc) {
		return false, nil
	}
	if !isComplimentaryPlanType(acc.planTypeSnapshot()) {
		return false, nil
	}
	eligible, err := pool.refreshAccountQuotaNow(acc)
	if err != nil {
		return false, fmt.Errorf("refresh quota: %w", err)
	}
	if eligible {
		return false, nil
	}
	return isExhaustedComplimentaryAccount(acc), nil
}

func exhaustedComplimentaryAccounts(pool *AccountPool) []*Account {
	if pool == nil {
		return nil
	}
	accounts := make([]*Account, 0)
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	for _, acc := range pool.accounts {
		if isExhaustedComplimentaryAccount(acc) {
			accounts = append(accounts, acc)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].UserEmail != accounts[j].UserEmail {
			return accounts[i].UserEmail < accounts[j].UserEmail
		}
		accounts[i].EnsureAccountID()
		accounts[j].EnsureAccountID()
		return accounts[i].AccountID < accounts[j].AccountID
	})
	return accounts
}

// HandleDeleteExhaustedComplimentaryAccounts permanently removes all Free,
// Personal, Plus, and Personal Pro accounts that are currently unavailable because their
// complimentary AI trial is exhausted. Other account health failures and all
// included-AI paid plans are left untouched.
func HandleDeleteExhaustedComplimentaryAccounts(pool *AccountPool, accountsDir string, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}
		releaseMutation, ok := beginAccountMutationRequest(w, pool)
		if !ok {
			return
		}
		defer releaseMutation()

		accounts := exhaustedComplimentaryAccounts(pool)
		deleted := make([]string, 0, len(accounts))
		failed := make(map[string]string)
		for _, item := range runAccountsParallel(accounts, 10, func(acc *Account) error {
			// Destructive cleanup requires a fresh plan + authoritative V1
			// confirmation. A network/schema failure always keeps the account.
			confirmed, err := confirmExhaustedComplimentaryAccount(pool, acc)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
			acc.EnsureAccountID()
			if acc.AccountID == "" {
				return deleteAccountByIdentity(pool, accountsDir, acc.UserEmail, acc.TokenV2)
			}
			return deleteAccountByAccountIdentity(pool, accountsDir, acc.AccountID, acc.UserEmail, acc.TokenV2)
		}) {
			if item.err != nil {
				failed[item.email] = item.err.Error()
				log.Printf("[delete-exhausted-trials] failed %s: %v", item.email, item.err)
				continue
			}
			// A recovered account returns nil and stays in the pool; count only
			// accounts that actually disappeared.
			remaining := pool.FindByAccountID(item.accountID)
			if item.accountID == "" {
				remaining = pool.GetByEmail(item.email)
			}
			if remaining == nil {
				deleted = append(deleted, item.email)
			}
		}
		sort.Strings(deleted)

		log.Printf("[delete-exhausted-trials] deleted=%d failed=%d", len(deleted), len(failed))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"matched": len(accounts),
			"deleted": len(deleted),
			"emails":  deleted,
			"failed":  failed,
		})
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
