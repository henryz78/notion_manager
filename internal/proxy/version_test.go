package proxy

import "testing"

func TestCurrentBuildVersionPrecedence(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	BuildVersion = "linked-version"

	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "railway-sha")
	t.Setenv("APP_VERSION", "operator-version")
	if got := CurrentBuildVersion(); got != "operator-version" {
		t.Fatalf("APP_VERSION precedence: got %q", got)
	}

	t.Setenv("APP_VERSION", "")
	if got := CurrentBuildVersion(); got != "railway-sha" {
		t.Fatalf("Railway version precedence: got %q", got)
	}

	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")
	if got := CurrentBuildVersion(); got != "linked-version" {
		t.Fatalf("linked version fallback: got %q", got)
	}
}
