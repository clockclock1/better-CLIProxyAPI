package executor

import "testing"

func TestParseCursorToolCallsVariants(t *testing.T) {
	cases := []struct {
		name, input string
		want        int
	}{
		{"bare", `{"tool_calls":[{"name":"read_file","arguments":{"path":"a.txt"}}]}`, 1},
		{"fenced", "```json\n{\"tool_calls\":[{\"name\":\"read_file\",\"arguments\":{\"path\":\"a.txt\"}}]}\n```", 1},
		{"with prose", `Sure, calling it now: {"tool_calls":[{"name":"read_file","arguments":{"path":"a.txt"}}]}`, 1},
		{"two calls", `{"tool_calls":[{"name":"a","arguments":{}},{"name":"b","arguments":{}}]}`, 2},
		{"no arguments", `{"tool_calls":[{"name":"a"}]}`, 1},
		{"plain text", `I'll read that file for you.`, 0},
		{"empty", ``, 0},
		{"bad json", `{"tool_calls":[}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := parseCursorToolCalls(tc.input)
			if tc.want == 0 {
				if ok {
					t.Fatalf("expected no calls, got %+v", calls)
				}
				return
			}
			if !ok {
				t.Fatal("expected calls")
			}
			if len(calls) != tc.want {
				t.Fatalf("got %d calls, want %d", len(calls), tc.want)
			}
		})
	}
}
