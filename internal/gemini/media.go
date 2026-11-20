package gemini

import (
	"encoding/base64"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// MediaAsset is one image or video the model produced.
//
// Gemini web has no single media contract across origins: consumer
// StreamGenerate inlines base64 image parts inside its RPC array, Discovery
// Engine wraps the answer in groundedContent, and the browser bridge scrapes
// whatever the DOM rendered. Rather than guess a position per transport, media
// is recognised by content — data URIs, file-header magic inside JSON strings,
// and media URLs on Google's asset hosts — so the same extractor works whichever
// transport carried it.
type MediaAsset struct {
	Kind   string `json:"kind"` // "image" | "video"
	MIME   string `json:"mime_type,omitempty"`
	URL    string `json:"url,omitempty"`
	Source string `json:"source,omitempty"` // "data-uri" | "inline" | "url"
	// DataB64 is the raw base64 payload (no data: prefix) when the media arrived
	// inline. It is deliberately excluded from JSON: callers decide whether to
	// return a URL or an inlined b64_json.
	DataB64 string `json:"-"`
}

const (
	// maxMediaAssets bounds how much a single answer may return. A rendered chat
	// turn never carries more than a handful; anything beyond that is a parsing
	// accident rather than a real generation.
	maxMediaAssets = 16
	// minInlineMediaBytes rejects small base64 blobs (icons, tracking pixels,
	// truncated fragments) that would otherwise pass the magic check.
	minInlineMediaBytes = 256
)

var (
	reMediaDataURI = regexp.MustCompile(`data:(image|video)/([A-Za-z0-9.+-]+);base64,([A-Za-z0-9+/=]{64,})`)
	reMediaJSONB64 = regexp.MustCompile(`"([A-Za-z0-9+/]{256,}={0,2})"`)
	reMediaURL     = regexp.MustCompile("https?://[^\\s\"'\\\\<>`]+")
)

// mediaHostSuffixes are the hosts Google serves generated assets from. Anything
// else is ignored: a citation link or a repository URL is not media, and
// blindly returning it as an "image" would be worse than returning nothing.
var mediaHostSuffixes = []string{
	"googleusercontent.com",
	"ggpht.com",
	"googleapis.com",
	"gstatic.com",
}

var mediaVideoExt = map[string]bool{
	".mp4": true, ".m4v": true, ".webm": true, ".mov": true, ".mkv": true, ".m3u8": true,
}

var mediaImageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".avif": true, ".bmp": true,
}

// ExtractMedia finds every image and video carried by a raw upstream payload or
// an assembled answer. It never invents an asset: a payload with no media
// returns nothing, which lets callers report "the model answered with text"
// instead of fabricating a result.
func ExtractMedia(raw string) []MediaAsset {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	// JSON escapes slashes in URLs and quotes around base64 fields; unescaping
	// once keeps a single code path for the raw payload and the parsed text.
	text := strings.ReplaceAll(raw, `\/`, "/")
	text = strings.ReplaceAll(text, `\"`, `"`)

	var out []MediaAsset
	seen := stringSet{}

	add := func(asset MediaAsset) {
		if len(out) >= maxMediaAssets {
			return
		}
		key := asset.URL
		if key == "" {
			// Identify inline payloads by their first bytes: re-rendered deltas
			// repeat the same image and must not be returned twice.
			data := asset.DataB64
			if len(data) > 96 {
				data = data[:96]
			}
			key = asset.MIME + ":" + data
		}
		if seen.add(key) {
			out = append(out, asset)
		}
	}

	for _, match := range reMediaDataURI.FindAllStringSubmatch(text, -1) {
		kind, sub, data := match[1], match[2], match[3]
		if base64.StdEncoding.DecodedLen(len(data)) < minInlineMediaBytes {
			continue
		}
		add(MediaAsset{Kind: kind, MIME: kind + "/" + sub, DataB64: data, Source: "data-uri"})
	}

	for _, match := range reMediaJSONB64.FindAllStringSubmatch(text, -1) {
		data := match[1]
		mime, kind, ok := sniffInlineMediaMIME(data)
		if !ok {
			continue
		}
		if base64.StdEncoding.DecodedLen(len(data)) < minInlineMediaBytes {
			continue
		}
		add(MediaAsset{Kind: kind, MIME: mime, DataB64: data, Source: "inline"})
	}

	for _, candidate := range reMediaURL.FindAllString(text, -1) {
		cleaned := strings.TrimRight(candidate, ".,;:!?)]}")
		kind, mime, ok := classifyMediaURL(cleaned)
		if !ok {
			continue
		}
		add(MediaAsset{Kind: kind, MIME: mime, URL: cleaned, Source: "url"})
	}

	return out
}

// FilterMedia keeps the assets of one kind ("image" or "video").
func FilterMedia(assets []MediaAsset, kind string) []MediaAsset {
	if kind == "" {
		return assets
	}
	out := make([]MediaAsset, 0, len(assets))
	for _, asset := range assets {
		if asset.Kind == kind {
			out = append(out, asset)
		}
	}
	return out
}

// MergeMedia concatenates extraction passes and drops duplicates, so a payload
// scanned raw and its assembled text can be combined without returning the same
// file twice.
func MergeMedia(lists ...[]MediaAsset) []MediaAsset {
	var out []MediaAsset
	seen := stringSet{}
	for _, list := range lists {
		for _, asset := range list {
			key := asset.URL
			if key == "" {
				data := asset.DataB64
				if len(data) > 96 {
					data = data[:96]
				}
				key = asset.MIME + ":" + data
			}
			if seen.add(key) {
				out = append(out, asset)
				if len(out) >= maxMediaAssets {
					return out
				}
			}
		}
	}
	return out
}

// sniffInlineMediaMIME reads the file-header magic that survives base64
// encoding. Only formats Gemini actually emits are listed, so a random long
// JSON string is not mistaken for an image.
func sniffInlineMediaMIME(data string) (mime, kind string, ok bool) {
	switch {
	case strings.HasPrefix(data, "iVBORw0KGgo"):
		return "image/png", "image", true
	case strings.HasPrefix(data, "/9j/"):
		return "image/jpeg", "image", true
	case strings.HasPrefix(data, "R0lGOD"):
		return "image/gif", "image", true
	case strings.HasPrefix(data, "UklGR"):
		return "image/webp", "image", true
	case strings.HasPrefix(data, "GkXf"):
		return "video/webm", "video", true
	}
	// ISO base media container: decode the first bytes and look for the "ftyp"
	// box at offset 4. Base64 alignment shifts the box name, so matching the
	// encoded text directly does not work.
	if head := decodeB64Prefix(data, 12); len(head) >= 8 && string(head[4:8]) == "ftyp" {
		return "video/mp4", "video", true
	}
	return "", "", false
}

// decodeB64Prefix decodes the first n bytes of a base64 string, tolerating
// truncation mid-quantum.
func decodeB64Prefix(data string, n int) []byte {
	if len(data) > 64 {
		data = data[:64]
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Truncated input: drop trailing characters until it decodes.
		for trim := 1; trim <= 3 && len(data) > trim; trim++ {
			if decoded, err = base64.StdEncoding.DecodeString(data[:len(data)-trim]); err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil
	}
	if len(decoded) > n {
		return decoded[:n]
	}
	return decoded
}

// classifyMediaURL decides whether a URL points at media. Extension-less asset
// hosts are the norm for generated images (lh3.googleusercontent.com/gg/...),
// so those count as images unless the path says video.
func classifyMediaURL(raw string) (kind, mime string, ok bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	trusted := false
	for _, suffix := range mediaHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			trusted = true
			break
		}
	}
	if !trusted {
		return "", "", false
	}

	ext := strings.ToLower(path.Ext(parsed.Path))
	switch {
	case mediaVideoExt[ext]:
		return "video", "video/" + strings.TrimPrefix(ext, "."), true
	case mediaImageExt[ext]:
		mimeExt := strings.TrimPrefix(ext, ".")
		if mimeExt == "jpg" {
			mimeExt = "jpeg"
		}
		return "image", "image/" + mimeExt, true
	}

	lowerPath := strings.ToLower(parsed.Path)
	switch {
	case strings.Contains(lowerPath, "/video"), strings.Contains(lowerPath, "videogen"):
		return "video", "video/mp4", true
	case strings.Contains(lowerPath, "/image"), strings.Contains(lowerPath, "/gg/"),
		strings.Contains(lowerPath, "/a/"), strings.HasPrefix(host, "lh"):
		// lh3.googleusercontent.com/gg/... is the shape Gemini web uses for
		// generated images.
		return "image", "image/png", true
	}
	return "", "", false
}

// stringSet is a tiny ordered-uniqueness helper: insertion order matters because
// media is returned in the order the model produced it.
type stringSet map[string]bool

func (s stringSet) add(key string) bool {
	if key == "" || s[key] {
		return false
	}
	s[key] = true
	return true
}
