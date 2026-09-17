// Package identity derives stable session identities and unique request IDs.
//
// The upstream free tier fingerprints clients: requests whose
// x-opencode-session / x-opencode-request / x-opencode-project headers or
// User-Agent do not look like the official CLI are rejected with 403
// "OpenCode's free tier can only be used from within OpenCode".
// HAR captures of opencode 1.18.31 show the exact shapes:
//
//	User-Agent:         opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14
//	x-opencode-client:  cli
//	x-opencode-project: global
//	x-opencode-request: msg_<12 lowercase hex><14 base62>  (e.g. msg_0ad9f52f9001RS5CQes1JobvPt)
//	x-opencode-session: ses_<12 lowercase hex><14 base62>  (e.g. ses_f5260ad6fffeAxDCP8mCXKdAnl)
//
// The 12 hex digits are a 48-bit time-ordered prefix: the low 36 bits of the
// unix millisecond clock (9 hex digits) followed by a 12-bit counter
// (msg uses 0x001, ses uses 0xFFE for its descending order), verified by
// matching ID deltas against HAR wall-clock deltas. The absolute value is not
// validated server-side (HAR values decode to 1970/1972 yet return 200), so
// only the format must be byte-identical in shape.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"opencode2api/internal/jsonutil"
)

type RequestIDs struct {
	Session string
	Request string
	Project string
}

// base62Alphabet is the charset observed in official ID tails.
const base62Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// max36 masks the low 36 bits of the unix millisecond clock (9 hex digits).
const max36 = int64(0xFFFFFFFFF)

func DeriveRequestIDs(r *http.Request, body map[string]any) RequestIDs {
	// If the downstream caller is already the official CLI (or a faithful
	// replay), forward its session verbatim to preserve prompt-cache affinity
	// instead of re-hashing it into a non-official shape.
	incomingSession := strings.TrimSpace(r.Header.Get("x-opencode-session"))
	session := ""
	if isOfficialID(incomingSession, "ses") {
		session = incomingSession
	} else {
		signal := jsonutil.FirstString(
			incomingSession,
			r.Header.Get("x-session-affinity"),
			r.Header.Get("X-Session-Id"),
			r.Header.Get("x-session-id"),
			r.Header.Get("conversation-id"),
			jsonutil.StringAt(body, "conversation_id"),
			jsonutil.StringAt(body, "metadata", "session_id"),
		)
		if signal == "" {
			// Using the first user turn keeps a multi-turn conversation stable as its
			// history grows while separating conversations with different beginnings.
			signal = conversationSeed(body)
		}
		if signal == "" {
			signal = jsonutil.StringAt(body, "previous_response_id")
		}
		if signal == "" || signal == `{}` {
			signal = RandomID("fallback", 16)
		}
		session = StableSessionID(signal)
	}
	// Project: official CLI sends "global" outside a git worktree. The old
	// prj_<24hex> shape never appears in HAR captures and is part of the
	// fingerprint mismatch, so default to "global" and only forward an
	// explicitly provided downstream value.
	project := strings.TrimSpace(r.Header.Get("x-opencode-project"))
	if project == "" {
		project = strings.TrimSpace(jsonutil.StringAt(body, "metadata", "project_id"))
	}
	if project == "" {
		project = "global"
	}
	return RequestIDs{
		Session: session,
		Request: NewMessageID(),
		Project: project,
	}
}

// NewMessageID mints a fresh official-shaped request ID: msg_ + 9 hex digits
// of wall-clock low36 + 3 hex counter (0x001, as observed) + 14 random base62.
func NewMessageID() string {
	nowMS := time.Now().UnixMilli() & max36
	head := fmt.Sprintf("%09x%03x", nowMS, 1)
	return "msg_" + head + randomBase62(14)
}

// StableSessionID derives a stable, official-shaped session ID from an
// arbitrary signal (conversation seed). The output is deterministic so all
// turns of one conversation keep prompt-cache affinity, while matching the
// ses_<12hex><14base62> shape the free tier expects.
func StableSessionID(signal string) string {
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	head := hex.EncodeToString(sum[:6])
	tailSeed := sha256.Sum256([]byte("ses-tail\x00" + signal))
	return "ses_" + head + base62FromBytes(tailSeed[:], 14)
}

// isOfficialID reports whether value already looks like an official CLI ID
// (prefix + 12 lowercase hex + 14 base62), in which case it is forwarded.
func isOfficialID(value, prefix string) bool {
	rest, ok := strings.CutPrefix(value, prefix+"_")
	if !ok || len(rest) != 26 {
		return false
	}
	for i := 0; i < 12; i++ {
		c := rest[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	for i := 12; i < 26; i++ {
		c := rest[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

func randomBase62(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base62FromBytes(buf, n)
}

func base62FromBytes(buf []byte, n int) string {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = base62Alphabet[int(buf[i]%62)]
	}
	return string(out)
}

func conversationSeed(body map[string]any) string {
	if input, ok := body["input"].(string); ok && input != "" {
		return input
	}
	for _, field := range []string{"messages", "input"} {
		for _, raw := range jsonutil.SliceAt(body, field) {
			item, ok := raw.(map[string]any)
			if !ok || jsonutil.StringAt(item, "role") != "user" {
				continue
			}
			encoded, _ := json.Marshal(item["content"])
			if len(encoded) > 0 && string(encoded) != "null" {
				return string(encoded)
			}
		}
	}
	return ""
}

func StableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func RandomID(prefix string, size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
