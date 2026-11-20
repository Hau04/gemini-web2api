package server

// Enterprise (Discovery Engine) attachment strategy.
//
// Consumer Gemini Web has a file composer, so oversized prompts and referenced
// files are pushed through content-push and the model reads them natively. On a
// Discovery Engine origin the only lever is the widget context-file API, and the
// prompt itself is inlined into the streamAssist body. Inlining is what makes
// agent loops expensive: a coding agent re-sends the same 40 KB of file content
// on every turn, which is exactly how a session gets rate-limited or banned.
//
// So when uploads are possible we deliberately move that content out of the
// prompt and into attachments:
//   - files the latest user message references by path (PDF/Office/images and
//     long source files) are read from disk and attached;
//   - an oversized prompt, or large fenced code blocks inside it, is spilled
//     into a single .txt attachment and replaced by a short notice.
//
// Uploads are content-hash cached in the gemini client, so the same file is sent
// once and then referenced by fileId.

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// attachEnterpriseContext moves big referenced content out of the prompt and
// into Discovery Engine attachments. It is a no-op unless the origin is Gemini
// Enterprise AND an upload transport actually exists — otherwise the caller keeps
// the inline behaviour that at least works.
func (a *App) attachEnterpriseContext(messages []models.OpenAIMessage, prompt string, images []format.Image) (string, []format.Image) {
	if a.Gem == nil || !a.Gem.UsesDiscoveryEngine() || !a.Gem.CanUploadEnterpriseFiles() {
		return prompt, images
	}
	if a.Cfg.EnterpriseAutoAttachLocalFiles {
		images = a.appendLocalFileAttachments(messages, images)
	}
	if a.Cfg.EnterpriseAttachLargePrompt {
		prompt, images = a.spillLargePrompt(prompt, images)
	}
	return prompt, images
}

// appendLocalFileAttachments reads files the latest user message references and
// returns them as attachments, skipping anything already attached.
func (a *App) appendLocalFileAttachments(messages []models.OpenAIMessage, images []format.Image) []format.Image {
	if !PromptMayReferenceLocalFiles(messages) {
		return images
	}
	minBytes := a.Cfg.EnterpriseAutoAttachMinBytes
	if minBytes <= 0 {
		minBytes = a.Cfg.BridgeLargeCodeUploadBytes
	}
	atts, _ := DiscoverLocalUploadFiles(messages, map[string]string{}, minBytes)
	if len(atts) == 0 {
		return images
	}

	seen := make(map[string]bool, len(images))
	for _, img := range images {
		if img.Filename != "" {
			seen[img.Filename+"\x00"+fmt.Sprint(len(img.Data))] = true
		}
	}

	var names []string
	for _, att := range atts {
		key := att.Filename + "\x00" + fmt.Sprint(att.Size)
		if seen[key] {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(att.DataBase64)
		if err != nil || len(data) == 0 {
			a.Logf("Enterprise attachment %q could not be decoded for upload: %v", att.Filename, err)
			continue
		}
		seen[key] = true
		images = append(images, format.Image{Data: data, MIME: att.MIME, Filename: att.Filename})
		names = append(names, fmt.Sprintf("%s (%d KB)", att.Filename, (att.Size+1023)/1024))
	}
	if len(names) > 0 {
		a.Logf("Enterprise: attaching %d referenced local file(s) as context files instead of inlining them: %s",
			len(names), strings.Join(names, ", "))
	}
	return images
}

// spillLargePrompt replaces an oversized prompt (or its large code blocks) with a
// compact version plus ONE .txt attachment, mirroring what the browser bridge
// does for the consumer composer.
func (a *App) spillLargePrompt(prompt string, images []format.Image) (string, []format.Image) {
	var (
		compact string
		spill   *bridge.Attachment
		reason  string
	)
	if a.Cfg.BridgeSpilloverChars > 0 && len(prompt) > a.Cfg.BridgeSpilloverChars {
		origLen := len(prompt)
		compact, spill = SpilloverOversizedPrompt(prompt, a.Cfg.BridgeSpilloverChars, false)
		reason = fmt.Sprintf("%d chars", origLen)
	}
	if spill == nil && a.Cfg.BridgeLargeCodeUploadBytes > 0 {
		origLen := len(prompt)
		compact, spill = ExtractLargeInlineBlocksToAttachment(prompt, a.Cfg.BridgeLargeCodeUploadBytes)
		reason = fmt.Sprintf("%d -> %d chars", origLen, len(compact))
	}
	if spill == nil || spill.DataBase64 == "" {
		return prompt, images
	}
	data, err := base64.StdEncoding.DecodeString(spill.DataBase64)
	if err != nil || len(data) == 0 {
		return prompt, images
	}
	a.Logf("Enterprise: offloading prompt content (%s) into context-file attachment %s (%d KB) — keeps the replayed prompt small",
		reason, spill.Filename, (spill.Size+1023)/1024)
	return compact, append(images, format.Image{Data: data, MIME: spill.MIME, Filename: spill.Filename})
}
