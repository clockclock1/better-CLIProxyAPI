package cursor

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Cursor's agent endpoint uses Connect streaming framing: a 5-byte header
// (1 flag byte + big-endian uint32 length) followed by a protobuf message.
// Flag bit 0 marks a gzip-compressed payload and bit 1 marks end-of-stream.
const (
	connectFlagCompressed = 1
	connectFlagEndStream  = 2
	connectFrameHeaderLen = 5
)

// uuidFallbackMu guards uuidFallbackSeq, used only when crypto/rand fails.
var (
	uuidFallbackMu  sync.Mutex
	uuidFallbackSeq uint64
)

// connectFrame wraps a protobuf payload in a Connect stream frame.
func connectFrame(payload []byte) []byte {
	framed := make([]byte, connectFrameHeaderLen+len(payload))
	framed[0] = 0
	binary.BigEndian.PutUint32(framed[1:], uint32(len(payload)))
	copy(framed[connectFrameHeaderLen:], payload)
	return framed
}

// connectEnvelope is one decoded Connect frame.
type connectEnvelope struct {
	Payload   []byte
	EndStream bool
}

// connectFrameReader incrementally decodes Connect frames from a stream.
type connectFrameReader struct {
	buf []byte
}

// NewConnectFrameReader returns a reader that tolerates arbitrary chunk splits.
func NewConnectFrameReader() *connectFrameReader {
	return &connectFrameReader{}
}

// Push appends raw bytes and returns every frame that is now complete.
func (r *connectFrameReader) Push(chunk []byte) ([]connectEnvelope, error) {
	if len(chunk) > 0 {
		r.buf = append(r.buf, chunk...)
	}
	frames := make([]connectEnvelope, 0, 4)
	for {
		if len(r.buf) < connectFrameHeaderLen {
			return frames, nil
		}
		flags := r.buf[0]
		length := int(binary.BigEndian.Uint32(r.buf[1:connectFrameHeaderLen]))
		if length < 0 {
			return frames, fmt.Errorf("cursor: negative frame length")
		}
		if len(r.buf) < connectFrameHeaderLen+length {
			return frames, nil
		}
		payload := r.buf[connectFrameHeaderLen : connectFrameHeaderLen+length]
		r.buf = r.buf[connectFrameHeaderLen+length:]

		if flags&connectFlagCompressed != 0 {
			decompressed, errGzip := gunzip(payload)
			if errGzip != nil {
				return frames, fmt.Errorf("cursor: gunzip frame: %w", errGzip)
			}
			payload = decompressed
		}
		frames = append(frames, connectEnvelope{
			Payload:   payload,
			EndStream: flags&connectFlagEndStream != 0,
		})
	}
}

func gunzip(payload []byte) ([]byte, error) {
	reader, errNew := gzip.NewReader(bytes.NewReader(payload))
	if errNew != nil {
		return nil, errNew
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

/* ---------- protobuf primitives ---------- */

func pbVarint(n uint64) []byte {
	out := make([]byte, 0, 10)
	for n > 0x7f {
		out = append(out, byte(n&0x7f)|0x80)
		n >>= 7
	}
	return append(out, byte(n))
}

func pbKey(field, wire int) []byte { return pbVarint(uint64(field<<3 | wire)) }

func pbString(field int, value string) []byte {
	raw := []byte(value)
	out := append(pbKey(field, 2), pbVarint(uint64(len(raw)))...)
	return append(out, raw...)
}

func pbMessage(field int, payload []byte) []byte {
	out := append(pbKey(field, 2), pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func pbUint(field int, value uint64) []byte {
	return append(pbKey(field, 0), pbVarint(value)...)
}

func pbConcat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// pbField is one decoded protobuf field.
type pbField struct {
	Num   int
	Wire  int
	Bytes []byte
	Value uint64
}

// iteratePbFields walks a protobuf message. Unknown wire types stop the walk so
// a malformed frame cannot loop forever.
func iteratePbFields(buf []byte) []pbField {
	fields := make([]pbField, 0, 16)
	pos := 0
	for pos < len(buf) {
		key, next, ok := readPbVarint(buf, pos)
		if !ok {
			return fields
		}
		pos = next
		num, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			value, nextValue, okValue := readPbVarint(buf, pos)
			if !okValue {
				return fields
			}
			pos = nextValue
			fields = append(fields, pbField{Num: num, Wire: wire, Value: value})
		case 2:
			length, nextLen, okLen := readPbVarint(buf, pos)
			if !okLen {
				return fields
			}
			pos = nextLen
			end := pos + int(length)
			if end < pos || end > len(buf) {
				return fields
			}
			fields = append(fields, pbField{Num: num, Wire: wire, Bytes: buf[pos:end]})
			pos = end
		case 5:
			pos += 4
		case 1:
			pos += 8
		default:
			return fields
		}
	}
	return fields
}

func readPbVarint(buf []byte, pos int) (uint64, int, bool) {
	var value uint64
	var shift uint
	for pos < len(buf) {
		b := buf[pos]
		pos++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, pos, true
		}
		shift += 7
		if shift > 63 {
			return 0, pos, false
		}
	}
	return 0, pos, false
}

/* ---------- request construction ---------- */

// RunRequest describes one agent run: the prompt plus the model binding.
type RunRequest struct {
	Prompt         string
	Model          string
	ModelParams    [][2]string
	WorkspacePath  string
	ConversationID string
	MessageID      string
	Mode           uint64
	ClientVersion  string
}

// modelMeta encodes agent.v1.ModelDetails (model_id plus repeated params).
func modelMeta(model string, params [][2]string) []byte {
	out := pbString(1, model)
	for _, param := range params {
		out = append(out, pbMessage(3, pbConcat(pbString(1, param[0]), pbString(2, param[1])))...)
	}
	return out
}

// BuildRunFrames assembles the Connect frames that start an agent run.
//
// Field numbers follow AgentClientMessage.run_request and the preemptive
// request_context exchange the CLI performs before the server asks for it.
func BuildRunFrames(req RunRequest) [][]byte {
	conversationID := req.ConversationID
	if conversationID == "" {
		conversationID = newUUID()
	}
	messageID := req.MessageID
	if messageID == "" {
		messageID = newUUID()
	}
	mode := req.Mode
	if mode == 0 {
		mode = 2 // ASK
	}

	userMessage := pbConcat(
		pbString(1, req.Prompt),
		pbString(2, messageID),
		pbString(3, ""),
		pbUint(4, mode),
	)
	conversationAction := pbMessage(2, pbMessage(1, pbMessage(1, userMessage)))

	meta := modelMeta(req.Model, req.ModelParams)
	runRequest := pbConcat(
		pbString(1, ""),
		conversationAction,
		pbMessage(4, nil),
		pbString(5, conversationID),
		pbMessage(9, meta),
		pbUint(12, 0),
		pbMessage(14, meta),
		pbString(16, conversationID),
	)

	workspace := req.WorkspacePath
	env := pbConcat(
		pbString(1, "win32"),
		pbString(2, workspace),
		pbString(3, "powershell"),
		pbString(10, "UTC"),
		pbString(11, workspace),
		pbUint(14, 1),
		pbUint(16, 1),
		pbUint(19, 0),
		pbUint(20, 0),
		pbString(21, workspace),
		pbUint(22, 0),
	)
	requestContext := pbMessage(2, pbMessage(10, pbMessage(1, pbMessage(1, pbMessage(4, env)))))

	frames := [][]byte{
		connectFrame(pbMessage(1, runRequest)),
		connectFrame(requestContext),
		connectFrame(pbMessage(5, pbString(1, ""))),
		connectFrame(pbMessage(3, pbString(3, ""))),
	}
	for n := 1; n <= 8; n++ {
		frames = append(frames, connectFrame(pbMessage(3, pbConcat(pbUint(1, uint64(n)), pbString(3, "")))))
	}
	return frames
}

// HeartbeatFrame keeps an otherwise idle stream alive.
func HeartbeatFrame() []byte {
	return connectFrame(pbMessage(7, nil))
}

/* ---------- response decoding ---------- */

// ServerEvent is one decoded agent server message.
type ServerEvent struct {
	TextDelta      string
	ThinkingDelta  string
	Error          *RunError
	ToolCall       *ToolCall
	RequestContext bool
}

// RunError is a terminal Connect end-stream error.
type RunError struct {
	Code    string
	Message string
	Detail  string
}

func (e *RunError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return fmt.Sprintf("cursor upstream %s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	if e.Message != "" {
		return fmt.Sprintf("cursor upstream %s: %s", e.Code, e.Message)
	}
	return "cursor upstream error"
}

// ToolCall is a decoded agent tool invocation.
type ToolCall struct {
	Name      string
	Arguments string
}

// DecodeServerEvent extracts the interesting parts of one AgentServerMessage.
// Unknown messages decode to an event with nothing set.
func DecodeServerEvent(payload []byte) ServerEvent {
	var event ServerEvent
	for _, top := range iteratePbFields(payload) {
		if top.Num != 1 || top.Wire != 2 {
			continue
		}
		// interaction_update's payload wraps the delta one level deeper:
		//   1 interaction_update { 1 text_delta { 1 text } }
		//   1 interaction_update { 4 thinking_delta { 1 text } }
		for _, update := range iteratePbFields(top.Bytes) {
			if update.Wire != 2 {
				continue
			}
			switch update.Num {
			case 1:
				if event.TextDelta == "" {
					event.TextDelta = leafText(update.Bytes)
				}
			case 4:
				if event.ThinkingDelta == "" {
					event.ThinkingDelta = leafText(update.Bytes)
				}
			}
		}
	}
	return event
}

// leafText extracts the text carried by a text_delta / thinking_delta message.
//
// The message is { text: <string> }, but Cursor has wrapped it in an extra layer
// across client versions. Pure "is this bytes a message" checks cannot detect a
// leaf: short ASCII text such as "HELLO" parses as protobuf by accident (0x48 is
// a valid field key). Descend only while the child decodes into exactly one
// length-delimited field, which a text payload does not.
func leafText(payload []byte) string {
	current := payload
	for depth := 0; depth < 8; depth++ {
		fields := iteratePbFields(current)
		if len(fields) == 0 {
			return ""
		}
		var next []byte
		for _, f := range fields {
			if f.Wire == 2 && f.Num == 1 {
				next = f.Bytes
				break
			}
		}
		if next == nil {
			return ""
		}
		child := iteratePbFields(next)
		// A single well-formed length-delimited child means there is another
		// wrapper level still to unwrap.
		if len(child) == 1 && child[0].Wire == 2 && len(child[0].Bytes) > 0 && looksLikeMessage(next) {
			current = next
			continue
		}
		return string(next)
	}
	return ""
}

// looksLikeMessage reports whether payload is plausibly a protobuf message
// rather than plain text. Text payloads are rejected because their bytes almost
// never consume the buffer exactly while using only known wire types.
func looksLikeMessage(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	consumed := 0
	pos := 0
	for pos < len(payload) {
		key, next, ok := readPbVarint(payload, pos)
		if !ok {
			return false
		}
		field := int(key >> 3)
		wire := int(key & 7)
		if field <= 0 || field > 512 {
			return false
		}
		pos = next
		switch wire {
		case 0:
			_, nextVal, okVal := readPbVarint(payload, pos)
			if !okVal {
				return false
			}
			pos = nextVal
		case 2:
			length, nextLen, okLen := readPbVarint(payload, pos)
			if !okLen {
				return false
			}
			pos = nextLen
			if pos+int(length) > len(payload) || int(length) < 0 {
				return false
			}
			pos += int(length)
		case 5:
			pos += 4
		case 1:
			pos += 8
		default:
			return false
		}
		consumed++
	}
	return consumed > 0 && pos == len(payload)
}
func nestedString(payload []byte, field int) (string, bool) {
	for _, f := range iteratePbFields(payload) {
		if f.Num == field && f.Wire == 2 {
			return string(f.Bytes), true
		}
	}
	return "", false
}

// DecodeEndStream parses a Connect end-stream trailer carrying an upstream error.
func DecodeEndStream(payload []byte) *RunError {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	decoded, errDecode := decodeConnectEndStream(payload)
	if errDecode != nil {
		return &RunError{Message: string(payload)}
	}
	return decoded
}

// decodeConnectEndStream parses the JSON trailer Connect uses for errors.
func decodeConnectEndStream(payload []byte) (*RunError, error) {
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Type  string `json:"type"`
				Debug struct {
					Error   string `json:"error"`
					Details struct {
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"details"`
				} `json:"debug"`
			} `json:"details"`
		} `json:"error"`
	}
	if errJSON := json.Unmarshal(payload, &trailer); errJSON != nil {
		return nil, errJSON
	}
	if trailer.Error == nil {
		return nil, nil
	}
	runErr := &RunError{Code: trailer.Error.Code, Message: trailer.Error.Message}
	for _, detail := range trailer.Error.Details {
		if detail.Debug.Details.Title != "" {
			runErr.Detail = detail.Debug.Details.Title
		}
		if detail.Debug.Error != "" {
			if runErr.Detail == "" {
				runErr.Detail = detail.Debug.Error
			}
		}
	}
	if runErr.Message == "" && runErr.Detail == "" {
		return nil, nil
	}
	return runErr, nil
}

// newUUID returns a random RFC 4122 v4 UUID string.
func newUUID() string {
	raw := make([]byte, 16)
	if _, errRead := rand.Read(raw); errRead != nil {
		// Fall back to time plus a process-wide counter: two calls in the same
		// clock tick must not produce the same identifier.
		uuidFallbackMu.Lock()
		uuidFallbackSeq++
		seq := uuidFallbackSeq
		uuidFallbackMu.Unlock()
		binary.BigEndian.PutUint64(raw[0:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(raw[8:16], seq)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
