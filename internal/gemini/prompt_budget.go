package gemini

import (
	"fmt"
	"strings"
)

// Discovery Engine rejects a prompt it considers too large with a bare
// "Request contains an invalid argument" plus the machine-readable reason
// PROMPT_TOO_LARGE. The cap is not published and differs per tenant/model, so it
// is discovered at runtime and remembered: the first oversized turn pays one
// failed round trip, every later turn is trimmed before it is sent.
//
// Trimming drops the middle of the prompt — the oldest conversation turns — and
// keeps the head (system prompt, instructions, attached-file preamble) and the
// tail (recent turns, the current question). Instructions must never be what is
// lost: that is what the previous "spill it into a context file" strategy did,
// and the model could not read the file.

const (
	promptTooLargeMarkerReserve = 240
	// promptCeilingFloor is the smallest cap worth believing. A tenant that
	// refuses a prompt this small is not refusing it for its size — something
	// else in the request (typically the session's accumulated history) is what
	// is too large, and shrinking the prompt only digs a hole: the ceiling
	// ratchets down, every later turn is trimmed to nothing, and the hole never
	// recovers. Refusals below the floor are therefore not learned from.
	promptCeilingFloor = 8 << 10
	// maxPromptShrinkRetries bounds the shrink-and-retry loop: each round trip
	// costs seconds, and one or two 25% cuts either fit or the tenant wants a
	// prompt far smaller than a conversation can be.
	maxPromptShrinkRetries = 3
)

// promptTooLarge reports whether an upstream body is the PROMPT_TOO_LARGE refusal.
func promptTooLarge(body []byte) bool {
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "prompt_too_large") {
		return true
	}
	return strings.Contains(lower, "invalid argument") &&
		(strings.Contains(lower, "prompt") || strings.Contains(lower, "too large"))
}

// fitPromptForUpstream shortens a prompt to limit bytes, keeping both ends.
// ok reports whether anything was removed.
func fitPromptForUpstream(prompt string, limit int) (string, bool) {
	if limit <= 0 || len(prompt) <= limit {
		return prompt, false
	}
	// Too small for a marker: the current question is the only part worth
	// keeping, and it must fit — a prompt that overshoots the limit it was
	// trimmed to would be refused again with no way out.
	if limit <= promptTooLargeMarkerReserve {
		start := len(prompt) - limit
		if start < 0 {
			start = 0
		}
		return prompt[start:], true
	}
	budget := limit - promptTooLargeMarkerReserve
	headBudget := budget * 2 / 5
	tailBudget := budget - headBudget
	if tailBudget < 0 {
		tailBudget = 0
	}

	headEnd := cutAfterLine(prompt, headBudget)
	tailStart := cutBeforeLine(prompt, len(prompt)-tailBudget)
	if tailStart < headEnd {
		tailStart = headEnd
	}
	head := prompt[:headEnd]
	tail := prompt[tailStart:]
	omitted := len(prompt) - len(head) - len(tail)
	if omitted <= 0 {
		return prompt, false
	}
	marker := fmt.Sprintf("\n\n[... %d characters of earlier conversation omitted: the upstream rejected the full prompt as too large. The instructions above and the latest messages below are complete ...]\n\n", omitted)
	out := head + marker + tail
	if len(out) > limit {
		// Never cut the tail: it holds the current question. Trim the head.
		overflow := len(out) - limit
		if overflow < len(head) {
			head = head[:len(head)-overflow]
			out = head + marker + tail
		}
		if len(out) > limit {
			out = tail
			if len(out) > limit {
				out = out[len(out)-limit:]
			}
		}
	}
	return out, true
}

// cutAfterLine returns the last offset <= budget that ends a line.
func cutAfterLine(s string, budget int) int {
	if budget >= len(s) {
		return len(s)
	}
	if budget <= 0 {
		return 0
	}
	if idx := strings.LastIndexByte(s[:budget], '\n'); idx > 0 {
		return idx + 1
	}
	return budget
}

// cutBeforeLine returns the first offset >= start that begins a line.
func cutBeforeLine(s string, start int) int {
	if start <= 0 {
		return 0
	}
	if start >= len(s) {
		return len(s)
	}
	if idx := strings.IndexByte(s[start:], '\n'); idx >= 0 {
		return start + idx + 1
	}
	return start
}

// promptCeiling reports the effective prompt limit: the configured value, or the
// one learned from an upstream refusal, whichever is smaller.
func (c *Client) promptCeiling() int {
	configured := c.Cfg.EnterpriseMaxPromptChars
	c.promptMu.Lock()
	learned := c.learnedPromptCeiling
	c.promptMu.Unlock()
	switch {
	case configured > 0 && learned > 0:
		if learned < configured {
			return learned
		}
		return configured
	case configured > 0:
		return configured
	default:
		return learned
	}
}

// rememberPromptCeiling stores a limit the tenant has just enforced, so later
// turns are trimmed before they are sent instead of after a 400.
func (c *Client) rememberPromptCeiling(limit int) {
	if limit < promptCeilingFloor {
		return
	}
	c.promptMu.Lock()
	if c.learnedPromptCeiling == 0 || limit < c.learnedPromptCeiling {
		c.learnedPromptCeiling = limit
	}
	c.promptMu.Unlock()
}

// fitPrompt trims the prompt to the known ceiling, logging when it does.
func (c *Client) fitPrompt(prompt string) string {
	trimmed, ok := fitPromptForUpstream(prompt, c.promptCeiling())
	if !ok {
		return prompt
	}
	c.Logf("Enterprise: prompt trimmed from %d to %d chars (oldest turns omitted) to stay under the upstream limit", len(prompt), len(trimmed))
	return trimmed
}
