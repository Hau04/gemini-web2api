package gemini

import (
	"encoding/base64"
	"strings"
	"testing"
)

// fakePNG builds a base64 payload that really starts with the PNG signature, so
// it exercises the same magic check a real inline image would.
func fakePNG(size int) string {
	buf := make([]byte, 0, size+8)
	buf = append(buf, 0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n')
	for len(buf) < size+8 {
		buf = append(buf, 0)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestExtractMediaInlineBase64Image(t *testing.T) {
	png := fakePNG(4096)
	payload := `[["wrb.fr",null,"{\"candidates\":[{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"` + png + `\"}}]}]}"]]`

	assets := ExtractMedia(payload)
	if len(assets) != 1 {
		t.Fatalf("expected exactly one asset, got %d: %+v", len(assets), assets)
	}
	if assets[0].Kind != "image" || assets[0].MIME != "image/png" {
		t.Errorf("wrong classification: %+v", assets[0])
	}
	if assets[0].DataB64 != png {
		t.Errorf("inline payload was not preserved")
	}
	if assets[0].Source != "inline" {
		t.Errorf("expected source=inline, got %q", assets[0].Source)
	}
}

func TestExtractMediaDataURI(t *testing.T) {
	png := fakePNG(2048)
	text := "Here you go: ![cat](data:image/png;base64," + png + ")"

	assets := ExtractMedia(text)
	if len(assets) != 1 || assets[0].Kind != "image" || assets[0].Source != "data-uri" {
		t.Fatalf("data URI not recognised: %+v", assets)
	}
	if assets[0].DataB64 != png {
		t.Errorf("data URI payload mangled")
	}
}

func TestExtractMediaGoogleAssetURLs(t *testing.T) {
	text := "Picture: https://lh3.googleusercontent.com/gg/AIJ2gl9EXAMPLE \\u2014 video: https://storage.googleapis.com/gemini-video/veo/demo.mp4"

	assets := ExtractMedia(text)
	if len(assets) != 2 {
		t.Fatalf("expected image + video, got %d: %+v", len(assets), assets)
	}
	if assets[0].Kind != "image" || assets[0].MIME != "image/png" {
		t.Errorf("extension-less lh3 URL should be an image: %+v", assets[0])
	}
	if assets[1].Kind != "video" || assets[1].MIME != "video/mp4" {
		t.Errorf(".mp4 should be a video: %+v", assets[1])
	}
}

func TestExtractMediaEscapedSlashesAndJSONWrapping(t *testing.T) {
	payload := `{"answer":{"state":"SUCCEEDED","groundedContent":{"content":{"text":"https:\/\/lh3.googleusercontent.com\/gg\/abc123"}}}}`

	assets := ExtractMedia(payload)
	if len(assets) != 1 {
		t.Fatalf("escaped-slash URL not found: %+v", assets)
	}
	if assets[0].URL != "https://lh3.googleusercontent.com/gg/abc123" {
		t.Errorf("URL not unescaped: %q", assets[0].URL)
	}
}

func TestExtractMediaIgnoresNonMedia(t *testing.T) {
	text := strings.Join([]string{
		"See https://github.com/Hau04/gemini-web2api/pull/62",
		"I could not generate that image.",
		`{"jwt":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJodHRwczovL3ZlcnRleGFpc2VhcmNoLmNsb3VkLmdvb2dsZSJ9.c2lnbmF0dXJl"}`,
	}, "\n")

	if assets := ExtractMedia(text); len(assets) != 0 {
		t.Fatalf("non-media content must not be reported as an asset: %+v", assets)
	}
}

func TestExtractMediaDedupesRepeatedDeltas(t *testing.T) {
	png := fakePNG(4096)
	payload := `"` + png + `" "` + png + `"`

	if assets := ExtractMedia(payload); len(assets) != 1 {
		t.Fatalf("re-rendered delta duplicated the image: %+v", assets)
	}
}

func TestExtractMediaRejectsTinyInlineBlobs(t *testing.T) {
	// An icon-sized PNG header would pass the magic check but is not a generation.
	tiny := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nAA"))
	if assets := ExtractMedia(`"` + tiny + `"`); len(assets) != 0 {
		t.Fatalf("tiny blob must not be reported as media: %+v", assets)
	}
}

func TestFilterAndMergeMedia(t *testing.T) {
	imageURL := MediaAsset{Kind: "image", URL: "https://lh3.googleusercontent.com/gg/a", Source: "url"}
	videoURL := MediaAsset{Kind: "video", URL: "https://storage.googleapis.com/v/demo.mp4", Source: "url"}

	filtered := FilterMedia([]MediaAsset{imageURL, videoURL}, "video")
	if len(filtered) != 1 || filtered[0].Kind != "video" {
		t.Fatalf("filter returned %+v", filtered)
	}

	merged := MergeMedia([]MediaAsset{imageURL}, []MediaAsset{imageURL, videoURL})
	if len(merged) != 2 {
		t.Fatalf("merge should drop the duplicate: %+v", merged)
	}
}

func TestSniffInlineMediaMIME(t *testing.T) {
	cases := map[string]string{
		"iVBORw0KGgoAAA":       "image/png",
		"/9j/4AAQSkZJRg":       "image/jpeg",
		"R0lGODlhAQABAIAA":     "image/gif",
		"UklGRiQAAABXRUJQ":     "image/webp",
		"GkXfo59ChARJ":         "video/webm",
		"AAAAIGZ0eXBpc29tAAAC": "video/mp4",
	}
	for prefix, want := range cases {
		mime, _, ok := sniffInlineMediaMIME(prefix)
		if !ok || mime != want {
			t.Errorf("sniffInlineMediaMIME(%q) = %q, %v; want %q", prefix, mime, ok, want)
		}
	}
	if _, _, ok := sniffInlineMediaMIME("plainbase64lookingstring"); ok {
		t.Errorf("non-media data must not sniff as media")
	}
}
