package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func fakeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

// Deterministic 400 must short-circuit: predicate true, and the peek must
// not consume the body.
func TestPeekDeterministic400(t *testing.T) {
	resp := fakeResp(400, `{"error":{"message":"unknown field foo"}}`)
	if !isNonRetryableClientResponse(resp, nil) {
		t.Fatal("expected non-retryable")
	}
	if peekStaleReasoningReference(resp) {
		t.Fatal("must not be stale")
	}
	if got := readAll(t, resp); !strings.Contains(got, "unknown field") {
		t.Fatalf("body not restored: %q", got)
	}
}

// Stale reasoning 400 must NOT short-circuit.
func TestPeekStaleReasoning400(t *testing.T) {
	resp := fakeResp(400, `{"error":{"message":"Referenced reasoning item 'rs_abc' was not found or has expired"}}`)
	if !isNonRetryableClientResponse(resp, nil) {
		t.Fatal("expected non-retryable")
	}
	if !peekStaleReasoningReference(resp) {
		t.Fatal("expected stale marker")
	}
	if got := readAll(t, resp); !strings.Contains(got, "rs_abc") {
		t.Fatalf("body not restored: %q", got)
	}
}

// Retryable statuses must keep scanning.
func TestRetryableNotShortCircuited(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502} {
		resp := fakeResp(status, "x")
		if isNonRetryableClientResponse(resp, nil) {
			t.Fatalf("status %d must remain retryable", status)
		}
	}
	if !bytes.Equal([]byte("x"), []byte(readAll(t, fakeResp(200, "x")))) {
		t.Fatal("sanity")
	}
}
