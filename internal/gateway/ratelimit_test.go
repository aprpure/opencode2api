package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencode2api/internal/identity"
	wire "opencode2api/internal/protocol"
)

// A 429 FreeUsageLimitError says nothing about the model: the probe must report
// it as rate limited so the caller keeps the previous availability record
// instead of showing the model as unavailable.
func TestProbeAttemptReportsRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached"}}`)
	}))
	defer server.Close()

	g := &Gateway{}
	ids := identity.RequestIDs{Session: "ses_e9dce96e9ffe1RLmIebQCK01k3"}
	result := g.probeAttempt(context.Background(), server.Client(), server.URL, anonymousZenKey, "deepseek-v4.1-flash-free", wire.Chat, ids, "")
	if !result.attempted {
		t.Fatal("probe did not attempt a request")
	}
	if !result.rateLimited || result.reason != "rate_limited" {
		t.Fatalf("probeAttempt = %+v, want rate_limited", result)
	}
}
