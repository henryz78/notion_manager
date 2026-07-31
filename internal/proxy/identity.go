package proxy

import (
	"fmt"
	"strings"

	"notion-manager/internal/accountstore"
)

// ComputeAccountID returns the canonical multi-workspace identity:
// lowercase hex SHA-256 of user_id + "\x00" + space_id.
func ComputeAccountID(userID, spaceID string) string {
	return accountstore.ComputeAccountID(userID, spaceID)
}

func accountIDFromRaw(raw map[string]interface{}) string {
	if raw == nil {
		return ""
	}
	userID, _ := raw["user_id"].(string)
	spaceID, _ := raw["space_id"].(string)
	if userID != "" && spaceID != "" {
		return ComputeAccountID(userID, spaceID)
	}
	if accountID, _ := raw["account_id"].(string); strings.TrimSpace(accountID) != "" {
		return strings.ToLower(strings.TrimSpace(accountID))
	}
	return ""
}

// EnsureAccountID populates acc.AccountID from UserID+SpaceID if empty.
// Safe to call on legacy JSON that lacks account_id.
func (acc *Account) EnsureAccountID() {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if acc.UserID != "" && acc.SpaceID != "" {
		computed := ComputeAccountID(acc.UserID, acc.SpaceID)
		if acc.AccountID != computed {
			acc.AccountID = computed
		}
	} else {
		normalized := strings.ToLower(strings.TrimSpace(acc.AccountID))
		if acc.AccountID != normalized {
			acc.AccountID = normalized
		}
	}
}

// isAccountID reports whether s is a canonical lowercase SHA-256 identity.
func isAccountID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ShortSpaceID returns the first 8 hex chars of SpaceID (safe for logs/DTO).
func (acc *Account) ShortSpaceID() string {
	if acc == nil || len(acc.SpaceID) < 8 {
		return acc.SpaceID
	}
	return acc.SpaceID[:8]
}

// AmbiguousEmailError is returned when an email lookup matches multiple workspaces.
type AmbiguousEmailError struct {
	Email string
	Count int
}

func (e *AmbiguousEmailError) Error() string {
	return fmt.Sprintf("ambiguous: %d workspaces found for email %s", e.Count, e.Email)
}
