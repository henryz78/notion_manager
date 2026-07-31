package accountstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAccountJSONReusesLegacyIdentityPath(t *testing.T) {
	dir := t.TempDir()
	accountID := ComputeAccountID("user-1", "space-1")
	legacyPath := filepath.Join(dir, "legacy@example.com.json")
	oldPayload := []byte(`{"token_v2":"old","user_id":"user-1","space_id":"space-1"}`)
	if err := os.WriteFile(legacyPath, oldPayload, 0o600); err != nil {
		t.Fatal(err)
	}

	newPayload := []byte(`{"token_v2":"new","user_id":"user-1","space_id":"space-1","account_id":"` + accountID + `"}`)
	path, err := WriteAccountJSON(dir, accountID, strings.Repeat("very-long-address-", 30)+"@example.com", newPayload)
	if err != nil {
		t.Fatalf("WriteAccountJSON: %v", err)
	}
	if path != legacyPath {
		t.Fatalf("path=%q want legacy path %q", path, legacyPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("files=%d want=1", len(entries))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if raw["token_v2"] != "new" {
		t.Fatalf("token_v2=%v want new", raw["token_v2"])
	}
}

func TestCanonicalFilenameBoundsLongLabel(t *testing.T) {
	name := CanonicalFilename(strings.Repeat("a", 64), strings.Repeat("b", 1000)+"@example.com")
	if len(name) > 255 {
		t.Fatalf("filename is %d bytes", len(name))
	}
	if !strings.HasSuffix(name, ".json") {
		t.Fatalf("filename=%q", name)
	}
}
