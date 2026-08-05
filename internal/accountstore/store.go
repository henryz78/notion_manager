package accountstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxFilenameLabelBytes = 120

var (
	directoryLocks sync.Map // absolute directory path -> *sync.Mutex
	pathLocks      sync.Map // absolute file path -> *sync.Mutex
)

// ComputeAccountID returns the stable identity for one user membership in one
// workspace. A single Notion credential can therefore own several account IDs.
func ComputeAccountID(userID, spaceID string) string {
	userID = strings.TrimSpace(userID)
	spaceID = strings.TrimSpace(spaceID)
	if userID == "" || spaceID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userID + "\x00" + spaceID))
	return hex.EncodeToString(sum[:])
}

// ComputeLoginID groups workspace memberships belonging to the same Notion
// user without exposing the raw user ID to the dashboard.
func ComputeLoginID(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("login\x00" + userID))
	return hex.EncodeToString(sum[:])
}

func lockFor(target string, locks *sync.Map) (func(), error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("resolve account store path: %w", err)
	}
	value, _ := locks.LoadOrStore(filepath.Clean(absolute), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
}

// LockDirectory serializes identity discovery with writes and deletes. Callers
// that also lock a file must always acquire the directory lock first.
func LockDirectory(dir string) (func(), error) {
	return lockFor(dir, &directoryLocks)
}

// LockPath serializes mutations to an individual account file.
func LockPath(path string) (func(), error) {
	return lockFor(path, &pathLocks)
}

// AccountIDFromJSON derives the canonical identity from user_id + space_id.
// The stored account_id is used only for legacy files that lack those fields.
func AccountIDFromJSON(data []byte) (string, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	userID, _ := raw["user_id"].(string)
	spaceID, _ := raw["space_id"].(string)
	if accountID := ComputeAccountID(userID, spaceID); accountID != "" {
		return accountID, nil
	}
	accountID, _ := raw["account_id"].(string)
	return strings.ToLower(strings.TrimSpace(accountID)), nil
}

// FilenameLabel returns a short ASCII-only suffix. The account ID is already
// unique, so retaining an unlimited email/name suffix only risks exceeding the
// filesystem's per-component length limit.
func FilenameLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		switch {
		case r == '@', r == '.', r == '-', r == '_':
			return r
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(label))
	label = strings.Trim(label, "_")
	if label == "" {
		label = "account"
	}
	if len(label) > maxFilenameLabelBytes {
		label = strings.TrimRight(label[:maxFilenameLabelBytes], "._-")
		if label == "" {
			label = "account"
		}
	}
	return label
}

func CanonicalFilename(accountID, label string) string {
	label = FilenameLabel(label)
	if accountID == "" {
		return label + ".json"
	}
	return strings.ToLower(accountID) + "__" + label + ".json"
}

func findIdentityPathLocked(dir, accountID string) (string, error) {
	if accountID == "" {
		return "", nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var match string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("protect account file %s: %w", filepath.Base(path), err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		storedID, err := AccountIDFromJSON(data)
		if err != nil || storedID != accountID {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("multiple account files match account_id %s", accountID)
		}
		match = path
	}
	return match, nil
}

// WriteAccountJSON atomically upserts one workspace profile. It reuses a
// legacy filename for the same identity, preventing a canonical duplicate
// from being created beside it.
func WriteAccountJSON(dir, accountID, label string, data []byte) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create accounts dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("protect accounts dir: %w", err)
	}
	unlockDirectory, err := LockDirectory(dir)
	if err != nil {
		return "", err
	}
	defer unlockDirectory()

	path, err := findIdentityPathLocked(dir, accountID)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = filepath.Join(dir, CanonicalFilename(accountID, label))
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil && !os.IsNotExist(chmodErr) {
			return "", fmt.Errorf("protect existing account file %s: %w", filepath.Base(path), chmodErr)
		}
		if existing, readErr := os.ReadFile(path); readErr == nil {
			existingID, parseErr := AccountIDFromJSON(existing)
			if parseErr != nil {
				return "", fmt.Errorf("parse existing account file %s: %w", filepath.Base(path), parseErr)
			}
			if accountID != "" && existingID != accountID {
				return "", fmt.Errorf("account filename collision at %s", filepath.Base(path))
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read existing account file %s: %w", filepath.Base(path), readErr)
		}
	}

	unlockFile, err := LockPath(path)
	if err != nil {
		return "", err
	}
	defer unlockFile()

	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create account temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("protect account temp file: %w", err)
	}
	payload := data
	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		payload = append(append([]byte(nil), payload...), '\n')
	}
	if _, err := tmpFile.Write(payload); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("write account temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("sync account temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close account temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("replace account file: %w", err)
	}
	cleanup = false
	return path, nil
}
