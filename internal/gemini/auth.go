package gemini

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type CookieInfo struct {
	Cookie    string
	SAPISID   string
	AuthUser  string
	XSRFToken string
	GeminiBL  string
	// BaseURL is the origin of the tab the extension harvested this session from
	// (e.g. https://vertexaisearch.cloud.google.com). XSRF tokens are
	// origin-specific, so direct-mode requests must target this origin, not
	// necessarily the configured base_url.
	BaseURL   string
}

type CookieCache struct {
	mu    sync.Mutex
	file  string
	mtime time.Time
	info  CookieInfo
}

func NewCookieCache(file string) *CookieCache {
	return &CookieCache{file: file}
}

func (c *CookieCache) Load() (CookieInfo, error) {
	if c.file == "" {
		return CookieInfo{}, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	stat, err := os.Stat(c.file)
	if err != nil {
		if c.info.Cookie != "" {
			log.Printf("Cookie load error: %v", err)
			return c.info, nil
		}
		return CookieInfo{}, err
	}

	if stat.ModTime().Equal(c.mtime) && c.info.Cookie != "" {
		return c.info, nil
	}

	contentBytes, err := os.ReadFile(c.file)
	if err != nil {
		log.Printf("Cookie load error: %v", err)
		return c.info, nil
	}

	content := strings.TrimSpace(string(contentBytes))
	var cookieStr string
	var sapisid string
	var authUser string
	var xsrfToken string
	var geminiBL string
	var baseURL string

	if strings.HasPrefix(content, "{") {
		var data struct {
			Cookie    string          `json:"cookie"`
			SAPISID   string          `json:"sapisid"`
			AuthUser  json.RawMessage `json:"auth_user"`
			XSRFToken *string         `json:"xsrf_token"`
			GeminiBL  *string         `json:"gemini_bl"`
			BaseURL   *string         `json:"base_url"`
		}
		if err := json.Unmarshal([]byte(content), &data); err == nil {
			cookieStr = data.Cookie
			sapisid = data.SAPISID
			if len(data.AuthUser) > 0 {
				u := strings.Trim(strings.TrimSpace(string(data.AuthUser)), `"`)
				if u != "null" {
					authUser = u
				}
			}
			if data.XSRFToken != nil {
				xsrfToken = *data.XSRFToken
			}
			if data.GeminiBL != nil {
				geminiBL = *data.GeminiBL
			}
			if data.BaseURL != nil {
				baseURL = *data.BaseURL
			}
		}
	} else {
		cookieStr = content
	}

	if sapisid == "" && cookieStr != "" {
		pairs := strings.Split(cookieStr, "; ")
		for _, pair := range pairs {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 && parts[0] == "SAPISID" {
				sapisid = parts[1]
				break
			}
		}
	}

	c.mtime = stat.ModTime()
	c.info = CookieInfo{
		Cookie:    cookieStr,
		SAPISID:   sapisid,
		AuthUser:  authUser,
		XSRFToken: xsrfToken,
		GeminiBL:  geminiBL,
		BaseURL:   baseURL,
	}

	return c.info, nil
}

func SAPISIDHash(sapisid string) string {
	return SAPISIDHashWithOrigin(sapisid, "https://gemini.google.com")
}

func SAPISIDHashWithOrigin(sapisid, origin string) string {
	if sapisid == "" {
		return ""
	}
	if origin == "" {
		origin = "https://gemini.google.com"
	}
	return SAPISIDHashWithScope(sapisid, origin)
}

// SAPISIDHashWithScope computes the first-party SAPISIDHASH authorization value
// for an arbitrary scope string (the page origin for web RPCs, or an OAuth
// scope for googleapis endpoints).
func SAPISIDHashWithScope(sapisid, scope string) string {
	if sapisid == "" {
		return ""
	}
	ts := time.Now().Unix()
	input := fmt.Sprintf("%d %s %s", ts, sapisid, scope)
	h := sha1.Sum([]byte(input))
	return fmt.Sprintf("SAPISIDHASH %d_%s", ts, hex.EncodeToString(h[:]))
}

func AccountPrefix(authUser string) string {
	if authUser == "" {
		return ""
	}
	return "/u/" + authUser
}
