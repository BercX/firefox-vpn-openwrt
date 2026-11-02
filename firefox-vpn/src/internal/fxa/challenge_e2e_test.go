package fxa

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestSolveFastlyChallengeE2E checks that an API request passes the edge,
// solving a Fastly challenge if required by this exit IP. It hits live
// infrastructure, so it only runs when FXA_E2E=1 is set.
func TestSolveFastlyChallengeE2E(t *testing.T) {
	if os.Getenv("FXA_E2E") != "1" {
		t.Skip("set FXA_E2E=1 to run the live Fastly challenge test")
	}

	ctx := context.Background()
	// A dummy login must reach the FxA origin: it should fail with a
	// proper FxA error (e.g. unknown account), not with HTTP 406.
	_, err := fxaLogin(ctx, "nonexistent-user-e2e@example.invalid", "dummy-password", "")
	var apiErr *fxaAPIError
	if !errors.As(err, &apiErr) || apiErr.Status == http.StatusNotAcceptable || apiErr.Errno == 0 {
		t.Fatalf("expected a structured FxA error, not an edge/transport failure: %v", err)
	}
	t.Logf("dummy login result: %v", err)
}

// A syntactically valid but nonexistent token must reach OAuth, not Fastly's 406.
func TestRefreshTokenE2E(t *testing.T) {
	if os.Getenv("FXA_E2E") != "1" {
		t.Skip("set FXA_E2E=1 to run the live refresh test")
	}
	_, err := RefreshToken(context.Background(), strings.Repeat("0", 64))
	var apiErr *fxaAPIError
	if !errors.As(err, &apiErr) || apiErr.Status == http.StatusNotAcceptable || apiErr.Errno == 0 {
		t.Fatalf("expected a structured FxA error, not an edge/transport failure: %v", err)
	}
	t.Logf("dummy refresh result: %v", err)
}
