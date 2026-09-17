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

// Deterministic 400 (including stale-reasoning 400) short-circuits the
// anonymous scan: the outer doUpstream triggers its strip-and-retry off
// the final response body's marker, so scanning more proxies only burns
// round trips for the same rejection.
func TestDeterministic400ShortCircuits(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"unknown field foo"}}`,
		`{"error":{"message":"Referenced reasoning item 'rs_abc' was not found or has expired"}}`,
	} {
		resp := fakeResp(400, body)
		if !isNonRetryableClientResponse(resp, nil) {
			t.Fatalf("expected non-retryable for %q", body)
		}
		if got := readAll(t, resp); !strings.Contains(got, body[len(body)-20:]) {
			t.Fatalf("body must stay intact for outer retry: %q", got)
		}
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
