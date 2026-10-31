package executor

import (
	"strings"
)

// Mars-Sea request image budgets (issue #37): standing 32 MiB / 60 images.
const (
	commandCodeImageMaxBytes  = 32 * 1024 * 1024
	commandCodeImageMaxCount  = 60
	commandCodeImageOmitted   = "[image omitted: request image budget exceeded]"
)

type commandCodeImageRef struct {
	msgIdx   int
	partIdx  int
	byteSize int
}

// commandCodeOffloadImagesInPlace replaces oldest data-URL images with a text
// placeholder when the request exceeds the gateway ~50 MB body / Mars-Sea
// standing budget. Mutates CLI/OpenAI-style content arrays in place.
func commandCodeOffloadImagesInPlace(messages []any) {
	refs := make([]commandCodeImageRef, 0)
	totalBytes := 0
	for mi, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for pi, part := range content {
			block, ok := part.(map[string]any)
			if !ok {
				continue
			}
			size := commandCodeImagePartBytes(block)
			if size <= 0 {
				continue
			}
			refs = append(refs, commandCodeImageRef{msgIdx: mi, partIdx: pi, byteSize: size})
			totalBytes += size
		}
	}
	if len(refs) <= commandCodeImageMaxCount && totalBytes <= commandCodeImageMaxBytes {
		return
	}
	// Drop oldest first until under budget.
	for len(refs) > 0 && (len(refs) > commandCodeImageMaxCount || totalBytes > commandCodeImageMaxBytes) {
		ref := refs[0]
		refs = refs[1:]
		msg, _ := messages[ref.msgIdx].(map[string]any)
		content, _ := msg["content"].([]any)
		if ref.partIdx < 0 || ref.partIdx >= len(content) {
			continue
		}
		content[ref.partIdx] = map[string]any{"type": "text", "text": commandCodeImageOmitted}
		msg["content"] = content
		totalBytes -= ref.byteSize
	}
}

func commandCodeImagePartBytes(block map[string]any) int {
	switch block["type"] {
	case "image":
		if s, ok := block["image"].(string); ok && strings.HasPrefix(s, "data:") {
			return len(s)
		}
	case "image_url":
		switch v := block["image_url"].(type) {
		case string:
			if strings.HasPrefix(v, "data:") {
				return len(v)
			}
		case map[string]any:
			if s, ok := v["url"].(string); ok && strings.HasPrefix(s, "data:") {
				return len(s)
			}
		}
	}
	return 0
}

// commandCodeCLIImageShape converts a data URL into the official CLI wire
// image part: {type:image, image:dataURL, mimeType}.
func commandCodeCLIImageShape(dataURL string) map[string]any {
	mime := "image/png"
	if strings.HasPrefix(dataURL, "data:") {
		if i := strings.Index(dataURL, ";"); i > 5 {
			mime = dataURL[5:i]
		} else if i := strings.Index(dataURL, ","); i > 5 {
			mime = dataURL[5:i]
		}
	}
	if strings.TrimSpace(mime) == "" {
		mime = "image/png"
	}
	return map[string]any{
		"type":     "image",
		"image":    dataURL,
		"mimeType": mime,
	}
}
