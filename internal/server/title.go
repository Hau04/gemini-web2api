package server

import (
	"fmt"
	"strings"
)

// isTitleSummaryRequest checks if the incoming prompt is a WorkBuddy or similar client's
// automatic session title or topic generation request e.g.
// "[System instruction]: Generate a concise, sentence-case title (3-7 words)... <session> whats douyin </session>"
func isTitleSummaryRequest(prompt string) (bool, string) {
	if strings.Contains(prompt, "# Tool Use") {
		return false, ""
	}
	lower := strings.ToLower(prompt)
	isWorkBuddyTitle := (strings.Contains(lower, "generate a concise, sentence-case title") ||
		strings.Contains(lower, "generate a concise title") ||
		strings.Contains(lower, "title (3-7 words)") ||
		strings.Contains(lower, "captures the main topic or goal of this session")) &&
		strings.Contains(lower, "isnewtopic")

	isGenericAutoTitle := !isWorkBuddyTitle &&
		(strings.Contains(lower, "[system instruction]:") || strings.Contains(lower, "<session>")) &&
		(strings.Contains(lower, "generate a concise title") ||
			strings.Contains(lower, "generate a short title") ||
			strings.Contains(lower, "generate a brief title") ||
			strings.Contains(lower, "summarize this conversation into a") ||
			strings.Contains(lower, "title for this conversation")) &&
		(strings.Contains(lower, "<session>") ||
			strings.Contains(lower, "return only the title") ||
			strings.Contains(lower, "respond with only the title") ||
			strings.Contains(lower, "output only the title") ||
			strings.Contains(lower, "nothing else"))

	if isWorkBuddyTitle || isGenericAutoTitle {
		sessionText := ""
		start := strings.Index(lower, "<session>")
		end := strings.Index(lower, "</session>")
		if start != -1 && end != -1 && end > start && end <= len(prompt) {
			sessionText = strings.TrimSpace(prompt[start+len("<session>") : end])
		} else {
			// Fallback: look for the last non-instruction line
			lines := strings.Split(prompt, "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				l := strings.TrimSpace(lines[i])
				if l != "" && !strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "{") && !strings.HasPrefix(l, "<") {
					sessionText = l
					break
				}
			}
		}
		return true, sessionText
	}
	return false, ""
}

func buildFastTitleText(sessionContent string) string {
	clean := strings.TrimSpace(sessionContent)

	cleanLower := strings.ToLower(clean)
	starters := []string{
		"what is ", "whats ", "who is ", "how to ", "how do i ", "can you ", "tell me about ",
		"explain ", "why is ", "what are ",
	}
	for _, starter := range starters {
		if strings.HasPrefix(cleanLower, starter) {
			clean = strings.TrimSpace(clean[len(starter):])
			cleanLower = strings.ToLower(clean)
			break
		}
	}

	clean = strings.TrimRight(clean, "?!.,:;")

	words := strings.Fields(clean)
	if len(words) > 5 {
		words = words[:5]
	}
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			words[i] = strings.ToUpper(string(runes[:1])) + strings.ToLower(string(runes[1:]))
		}
	}

	mainTopic := strings.Join(words, " ")
	if mainTopic == "" {
		return "New Session"
	}
	if len(words) == 1 {
		return "Explanation of " + mainTopic
	}
	return mainTopic
}

// generateFastTitleJSON produces an immediate valid JSON title response for WorkBuddy title requests.
// e.g. `{"isNewTopic": true, "title": "Explanation of Douyin"}`
func generateFastTitleJSON(sessionContent string) string {
	return fmt.Sprintf(`{"isNewTopic": true, "title": %q}`, buildFastTitleText(sessionContent))
}

// generateFastTitleResponse returns WorkBuddy's JSON payload when `isNewTopic` is expected,
// or a concise plain-text title for generic client title-generation prompts.
func generateFastTitleResponse(prompt, sessionContent string) string {
	if strings.Contains(strings.ToLower(prompt), "isnewtopic") {
		return generateFastTitleJSON(sessionContent)
	}
	return buildFastTitleText(sessionContent)
}
