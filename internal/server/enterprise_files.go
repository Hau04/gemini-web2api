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
	"bytes"
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
		prompt, images = a.appendLocalFileAttachments(messages, prompt, images)
	}
	if a.Cfg.EnterpriseAttachLargePrompt {
		prompt, images = a.spillLargePrompt(prompt, images)
	}
	return prompt, images
}

// appendLocalFileAttachments reads files the latest user message references and
// returns them as attachments, skipping anything already attached.
//
// The prompt gets a short inventory instead of the file bodies. That inventory is
// what stops an edit loop from turning into read-after-write: it tells the model
// the attachment is the current on-disk version, so it can go straight to a
// targeted edit and see its own change on the next turn without a fetch tool.
func (a *App) appendLocalFileAttachments(messages []models.OpenAIMessage, prompt string, images []format.Image) (string, []format.Image) {
	if !PromptMayReferenceLocalFiles(messages) {
		return prompt, images
	}
	minBytes := a.Cfg.EnterpriseAutoAttachMinBytes
	if minBytes <= 0 {
		minBytes = a.Cfg.BridgeLargeCodeUploadBytes
	}
	atts, _ := DiscoverLocalUploadFiles(messages, map[string]string{}, minBytes)
	if len(atts) == 0 {
		return prompt, images
	}

	seen := make(map[string]bool, len(images))
	for _, img := range images {
		if img.Filename != "" {
			seen[img.Filename+"\x00"+fmt.Sprint(len(img.Data))] = true
		}
	}

	var names, notice []string
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
		notice = append(notice, describeAttachedFile(att, data))
	}
	if len(names) > 0 {
		a.Logf("Enterprise: attaching %d referenced local file(s) as context files instead of inlining them: %s",
			len(names), strings.Join(names, ", "))
	}
	if len(notice) == 0 {
		return prompt, images
	}
	return attachedFilesNotice(notice) + prompt, images
}

// describeAttachedFile renders one inventory line: path, size, line count for
// text, and the name the file is attached under.
func describeAttachedFile(att bridge.Attachment, data []byte) string {
	detail := fmt.Sprintf("%d KB", (att.Size+1023)/1024)
	if isTextualMIME(att.MIME) {
		lines := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		detail = fmt.Sprintf("%s lines, %s", formatCount(lines), detail)
	}
	return fmt.Sprintf("- %s — %s — attached as context file %q", att.Filename, detail, att.Filename)
}

// attachedFilesNotice is prepended so the model knows it already holds the files
// and must not spend a tool call re-reading them.
func attachedFilesNotice(entries []string) string {
	return "# Attached workspace files (already loaded — do not re-read)\n" +
		strings.Join(entries, "\n") + "\n" +
		"These attachments are the current on-disk version as of this request. Do not call read_file, grep or shell " +
		"cat to fetch their contents again, and do not paste their full contents back into a tool argument. Make the " +
		"change with your edit/write tool using a targeted patch (str_replace / line range) — the updated file is " +
		"re-attached automatically on the next turn, so no verification read is needed.\n\n"
}

func isTextualMIME(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "xml") ||
		strings.Contains(m, "yaml") || strings.Contains(m, "javascript")
}

func formatCount(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(r))
	}
	return string(out)
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
