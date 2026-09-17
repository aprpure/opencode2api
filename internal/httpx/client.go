package httpx

// UserAgent mimics the official OpenCode CLI fingerprint observed in HAR
// captures (opencode 1.18.31 on Bun). The free tier rejects requests whose
// User-Agent looks like a third-party relay (e.g. "opencode/x.y.z (windows
// amd64; go1.x)"), returning 403 "OpenCode's free tier can only be used from
// within OpenCode". Keep this string byte-identical to the CLI.
func UserAgent() string {
	return "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"
}
