package gemini

import "strings"

// A tenant counts the session's own history towards the same prompt limit as the
// request, and that history grows by a whole turn every time. A conversation that
// re-sends its full history in the prompt — an agent loop does exactly that — gets
// no memory out of the session and pays for it twice: the limit is hit earlier,
// and the request is refused only to succeed on the retry without a session.
//
// So a session is attached only while it is free to attach:
//   - the prompt is under enterprise_session_max_prompt_chars (it already carries
//     the conversation, so the session adds nothing but risk), and
//   - this conversation has not already been refused for that reason.

// DefaultSessionMaxPromptChars is the prompt size above which no session is
// attached. It sits well above a one-shot question and well below the point where
// the tenant's combined limit becomes reachable.
const DefaultSessionMaxPromptChars = 32 << 10

// sessionMaxPromptChars is the configured threshold, or the default.
func (c *Client) sessionMaxPromptChars() int {
	if v := c.Cfg.EnterpriseSessionMaxPromptChars; v > 0 {
		return v
	}
	return DefaultSessionMaxPromptChars
}

// skipSessionForPrompt reports whether a session should be left out because the
// prompt already carries the conversation, or because this conversation has
// already been refused for combining the two.
func (c *Client) skipSessionForPrompt(promptLen int, conversation string) (bool, string) {
	key := strings.TrimSpace(conversation)
	c.promptMu.Lock()
	refused := c.enterpriseSessionRefused[key] && key != ""
	c.promptMu.Unlock()
	if refused {
		return true, "the tenant already refused a session for this conversation"
	}
	if limit := c.sessionMaxPromptChars(); promptLen > limit {
		return true, "the prompt already carries the conversation"
	}
	return false, ""
}

// logSessionSkipped reports the first time a conversation goes without a session,
// once per conversation, so a long agent loop does not repeat it every turn.
func (c *Client) logSessionSkipped(conversation, why, session string) {
	key := strings.TrimSpace(conversation)
	c.promptMu.Lock()
	if c.sessionSkipLogged == nil {
		c.sessionSkipLogged = make(map[string]bool)
	}
	seen := c.sessionSkipLogged[key]
	c.sessionSkipLogged[key] = true
	c.promptMu.Unlock()
	if seen {
		return
	}
	if s := shortSessionName(session); s != "<none>" {
		c.Logf("Enterprise: not attaching session %s for conversation %s — %s; a session is reused only while the prompt is small", s, key, why)
		return
	}
	c.Logf("Enterprise: no session for conversation %s — %s; a session is reused only while the prompt is small", key, why)
}

// markSessionRefused remembers that this conversation must not attach a session,
// so the next turn does not pay another refused round trip to find out again.
func (c *Client) markSessionRefused(conversation string) {
	key := strings.TrimSpace(conversation)
	if key == "" {
		return
	}
	c.promptMu.Lock()
	if c.enterpriseSessionRefused == nil {
		c.enterpriseSessionRefused = make(map[string]bool)
	}
	c.enterpriseSessionRefused[key] = true
	c.promptMu.Unlock()
}
