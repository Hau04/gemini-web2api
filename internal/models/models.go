package models

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Model represents a Gemini backend routing configuration.
// Mode corresponds to the Gemini upstream mode ID, Think specifies the default reasoning depth,
// and Extra contains index-based overrides for Gemini's sparse RPC request array.
type Model struct {
	Mode   int         `json:"mode"`
	Think  int         `json:"think"`
	Desc   string      `json:"desc"`
	Header string      `json:"header,omitempty"`
	Extra  map[int]any `json:"extra,omitempty"`
}

var MODELS = map[string]Model{
	"gemini-4-argon": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Frontier model for complex tasks (Gemini 4 Argon, requires Google AI Pro cookie)",
	},
	"argon": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Alias for gemini-4-argon",
	},
	"gemini-4": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Alias for gemini-4-argon",
	},
	"gemini-4-hydrogen": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "High-speed frontier execution model (Gemini 4 Hydrogen)",
	},
	"hydrogen": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Alias for gemini-4-hydrogen",
	},
	"gemini-4-carbon": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Frontier model (Gemini 4 Carbon)",
	},
	"carbon": {
		Mode:   8,
		Think:  4,
		Header: "95a4c0abcba24364",
		Desc:   "Alias for gemini-4-carbon",
	},
	"gemini-3.8-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "Latest all-around model (Gemini 3.8 Flash)",
	},
	"gemini-3.7-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "All-around model (Gemini 3.7 Flash)",
	},
	"gemini-3.6-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "All-around model (Gemini 3.6 Flash)",
	},
	"gemini-3.5-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "Alias for gemini-3.6-flash (backend upgraded)",
	},
	"gemini-3.5-flash-thinking": {
		Mode:  2,
		Think: 0,
		Desc:  "Deep thinking mode, longest output (~20k chars)",
	},
	"gemini-3.1-pro": {
		Mode:  3,
		Think: 4,
		Desc:  "Pro model (requires cookie for real routing)",
	},
	"gemini-3.1-pro-enhanced": {
		Mode:  3,
		Think: 4,
		Desc:  "Pro with enhanced output (experimental)",
		Extra: map[int]any{31: 2, 80: 3},
	},
	"gemini-auto": {
		Mode:  4,
		Think: 4,
		Desc:  "Auto model selection",
	},
	"gemini-3.5-flash-thinking-lite": {
		Mode:  5,
		Think: 0,
		Desc:  "Dynamic thinking with adaptive depth",
	},
	"gemini-flash-lite": {
		Mode:  6,
		Think: 4,
		Desc:  "Lightweight fast model",
	},
}

type Resolved struct {
	Name     string
	Mode     int
	Think    int
	Header   string
	Fallback bool
	Extra    map[int]any
	// ChatMode carries the optional chat-session directive parsed from the model
	// suffix: "" (auto), "new" (@new — always start a fresh chat in the browser
	// tab), or "continue" (@continue — always append to the tab's currently open
	// chat, preserving its Gemini memory). Only meaningful in bridge mode; the
	// direct cookie mode is stateless per request.
	ChatMode string
}

const DefaultModelName = "gemini-3.6-flash"

// Resolve maps a requested model string to its corresponding backend configuration.
// It supports model name suffixes like "@think=N" (e.g. "gemini-3.6-flash@think=0")
// to dynamically override the model's default thinking depth without modifying the model mapping.
// Chat-session directives "@new" / "@continue" can be combined in any order:
// "gemini-4-hydrogen@think=0@new" or "gemini-4-hydrogen@continue".
func Resolve(modelName, defaultName string) (Resolved, error) {
	if defaultName == "" {
		defaultName = DefaultModelName
	}

	var thinkOverride *int
	chatMode := ""

	// Parse @directives in any order after the base model name.
	if strings.Contains(modelName, "@") {
		segs := strings.Split(modelName, "@")
		base := segs[0]
		var leftover []string
		for _, d := range segs[1:] {
			dl := strings.ToLower(strings.TrimSpace(d))
			switch {
			case strings.HasPrefix(dl, "think="):
				val, err := strconv.Atoi(dl[len("think="):])
				if err != nil {
					return Resolved{}, fmt.Errorf("invalid think level: %s", dl[len("think="):])
				}
				thinkOverride = &val
			case dl == "new" || dl == "newchat" || dl == "fresh":
				chatMode = "new"
			case dl == "continue" || dl == "cont":
				chatMode = "continue"
			default:
				// Unrecognized segment: keep it in the name so unknown models still
				// fall through to the default-model fallback exactly as before.
				leftover = append(leftover, d)
			}
		}
		if len(leftover) == 0 {
			modelName = base
		} else {
			modelName = base + "@" + strings.Join(leftover, "@")
		}
	}

	cfg, exists := MODELS[modelName]
	fallback := false
	if !exists {
		log.Printf("Unknown model '%s', falling back to '%s'", modelName, defaultName)
		modelName = defaultName
		cfg = MODELS[defaultName]
		fallback = true
	}

	thinkMode := cfg.Think
	if thinkOverride != nil {
		thinkMode = *thinkOverride
	}

	return Resolved{
		Name:     modelName,
		Mode:     cfg.Mode,
		Think:    thinkMode,
		Header:   cfg.Header,
		Fallback: fallback,
		Extra:    cfg.Extra,
		ChatMode: chatMode,
	}, nil
}
