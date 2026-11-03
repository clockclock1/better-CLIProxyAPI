package cursor

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectFrameRoundTrip(t *testing.T) {
	payload := []byte("hello")
	framed := connectFrame(payload)

	reader := NewConnectFrameReader()
	frames, errPush := reader.Push(framed)
	if errPush != nil {
		t.Fatalf("Push() error = %v", errPush)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if string(frames[0].Payload) != "hello" {
		t.Errorf("payload = %q", frames[0].Payload)
	}
}

// TestConnectFrameReaderPartialDelivers guards the incremental framing: TCP (and
// HTTP/2) may split a frame across reads at any byte boundary.
func TestConnectFrameReaderPartialDelivers(t *testing.T) {
	framed := connectFrame([]byte("split-me-please"))

	reader := NewConnectFrameReader()
	for i := 0; i < len(framed)-1; i++ {
		frames, errPush := reader.Push(framed[i : i+1])
		if errPush != nil {
			t.Fatalf("Push(byte %d) error = %v", i, errPush)
		}
		if len(frames) != 0 {
			t.Fatalf("byte %d produced %d frames early", i, len(frames))
		}
	}
	frames, errPush := reader.Push(framed[len(framed)-1:])
	if errPush != nil {
		t.Fatalf("final Push() error = %v", errPush)
	}
	if len(frames) != 1 || string(frames[0].Payload) != "split-me-please" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestConnectFrameReaderMultipleFrames(t *testing.T) {
	body := append(connectFrame([]byte("a")), connectFrame([]byte("bb"))...)
	body = append(body, connectFrame([]byte("ccc"))...)

	frames, errPush := NewConnectFrameReader().Push(body)
	if errPush != nil {
		t.Fatalf("Push() error = %v", errPush)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
}

// TestConnectFrameReaderGzip covers flag bit 0: Cursor gzips error trailers.
func TestConnectFrameReaderGzip(t *testing.T) {
	inner := []byte(`{"error":{"code":"resource_exhausted"}}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, errWrite := writer.Write(inner); errWrite != nil {
		t.Fatalf("gzip write: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("gzip close: %v", errClose)
	}

	framed := make([]byte, 5+compressed.Len())
	framed[0] = connectFlagCompressed | connectFlagEndStream
	binary.BigEndian.PutUint32(framed[1:], uint32(compressed.Len()))
	copy(framed[5:], compressed.Bytes())

	frames, errPush := NewConnectFrameReader().Push(framed)
	if errPush != nil {
		t.Fatalf("Push() error = %v", errPush)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames", len(frames))
	}
	if !frames[0].EndStream {
		t.Error("EndStream flag should survive")
	}
	if !bytes.Equal(frames[0].Payload, inner) {
		t.Errorf("payload = %q", frames[0].Payload)
	}
}

// TestDecodeServerEventTextDelta verifies the field path
// interaction_update(1).text_delta(1).text(1) that carries assistant output.
func TestDecodeServerEventTextDelta(t *testing.T) {
	// interaction_update { text_delta { text: "HELLO" } }
	textDelta := pbMessage(1, pbMessage(1, pbString(1, "HELLO")))
	message := pbMessage(1, pbMessage(1, textDelta))

	event := DecodeServerEvent(message)
	if event.TextDelta != "HELLO" {
		t.Errorf("TextDelta = %q, want HELLO", event.TextDelta)
	}
}

func TestDecodeServerEventThinkingDelta(t *testing.T) {
	// interaction_update { thinking_delta { text: "pondering" } }
	thinkingDelta := pbMessage(1, pbString(1, "pondering"))
	interactionUpdate := pbMessage(4, thinkingDelta)
	message := pbMessage(1, interactionUpdate)

	event := DecodeServerEvent(message)
	if event.ThinkingDelta != "pondering" {
		t.Errorf("ThinkingDelta = %q", event.ThinkingDelta)
	}
}

func TestDecodeServerEventIgnoresUnknown(t *testing.T) {
	if event := DecodeServerEvent([]byte{0x08, 0x01}); event.TextDelta != "" {
		t.Errorf("unexpected text %q", event.TextDelta)
	}
	if event := DecodeServerEvent(nil); event.TextDelta != "" {
		t.Errorf("nil payload should decode to an empty event")
	}
}

// TestDecodeEndStreamNotLoggedIn reproduces the shape Cursor returns when a
// session token has been revoked. The human title carries the real reason.
func TestDecodeEndStreamNotLoggedIn(t *testing.T) {
	trailer := []byte(`{
	  "error": {
	    "code": "unauthenticated",
	    "message": "Error",
	    "details": [{
	      "type": "aiserver.v1.ErrorDetails",
	      "debug": {
	        "error": "ERROR_NOT_LOGGED_IN",
	        "details": {"title": "Authentication error", "detail": "try logging out and back in"}
	      }
	    }]
	  }
	}`)

	runErr := DecodeEndStream(trailer)
	if runErr == nil {
		t.Fatal("DecodeEndStream() returned nil")
	}
	if runErr.Code != "unauthenticated" {
		t.Errorf("Code = %q", runErr.Code)
	}
	if runErr.Detail != "Authentication error" {
		t.Errorf("Detail = %q", runErr.Detail)
	}
	if !strings.Contains(runErr.Error(), "unauthenticated") {
		t.Errorf("Error() = %q", runErr.Error())
	}
}

func TestDecodeEndStreamEmpty(t *testing.T) {
	if runErr := DecodeEndStream(nil); runErr != nil {
		t.Errorf("nil payload should decode to nil, got %+v", runErr)
	}
}

func TestDecodeEndStreamNoErrorField(t *testing.T) {
	if runErr := DecodeEndStream([]byte(`{}`)); runErr != nil {
		t.Errorf("expected nil for a trailer without error, got %+v", runErr)
	}
}

// TestBuildRunFramesShape asserts the frames are individually well-formed and
// that the run request is present.
func TestBuildRunFramesShape(t *testing.T) {
	frames := BuildRunFrames(RunRequest{Prompt: "hi", Model: "default", WorkspacePath: "C:/work"})
	if len(frames) < 4 {
		t.Fatalf("got %d frames, want at least 4", len(frames))
	}
	for i, frame := range frames {
		if len(frame) < 5 {
			t.Fatalf("frame %d too short", i)
		}
		if frame[0] != 0 {
			t.Fatalf("frame %d flags = %d, want 0", i, frame[0])
		}
		length := int(binary.BigEndian.Uint32(frame[1:5]))
		if length != len(frame)-5 {
			t.Fatalf("frame %d length field = %d, actual %d", i, length, len(frame)-5)
		}
	}
}

func TestHeartbeatFrame(t *testing.T) {
	frame := HeartbeatFrame()
	if len(frame) < 5 {
		t.Fatal("heartbeat frame too short")
	}
	if frame[0] != 0 {
		t.Errorf("heartbeat flags = %d", frame[0])
	}
}

func TestNewUUIDShape(t *testing.T) {
	id := newUUID()
	if len(id) != 36 {
		t.Fatalf("uuid length = %d, want 36", len(id))
	}
	if id[14] != '4' {
		t.Errorf("uuid version nibble = %c, want 4", id[14])
	}
	if id == newUUID() {
		t.Error("uuid should not repeat")
	}
}

func TestPbVarintRoundTrip(t *testing.T) {
	for _, value := range []uint64{0, 1, 127, 128, 300, 1 << 20} {
		encoded := pbVarint(value)
		decoded, next, ok := readPbVarint(encoded, 0)
		if !ok || next != len(encoded) || decoded != value {
			t.Errorf("varint %d round trip -> %d, next=%d, ok=%v", value, decoded, next, ok)
		}
	}
}

func TestIteratePbFieldsMalformed(t *testing.T) {
	// A truncated length-delimited field must not panic or loop.
	if fields := iteratePbFields([]byte{0x0a, 0x10, 0x01}); len(fields) != 0 {
		t.Errorf("expected no fields from truncated input, got %d", len(fields))
	}
}

// TestDecodeEndStreamJSONShape documents why the trailer is parsed as JSON
// rather than protobuf: Connect carries errors as UTF-8 JSON.
func TestDecodeEndStreamJSONShape(t *testing.T) {
	trailer, _ := json.Marshal(map[string]any{
		"error": map[string]any{"code": "internal", "message": "boom"},
	})
	runErr := DecodeEndStream(trailer)
	if runErr == nil || runErr.Code != "internal" {
		t.Fatalf("runErr = %+v", runErr)
	}
}
