package httpx

import (
	"fmt"
	"runtime"
)

// UserAgent tracks the upstream format: opencode/<cli-version> (<os> <arch>;
// <runtime>). It intentionally no longer mimics the Bun CLI string; keep it in
// sync with upstream since the free tier fingerprints this header.
func UserAgent() string {
	return fmt.Sprintf("opencode/1.18.31 (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
}
