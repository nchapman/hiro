package api

import (
	"bytes"
	"testing"
)

func TestFilterReplayQueries(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text passes through", "hello world\n", "hello world\n"},
		{"sgr color kept", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"cursor move kept", "\x1b[10;20H", "\x1b[10;20H"},
		{"DA1 primary dropped", "before\x1b[cafter", "beforeafter"},
		{"DA2 secondary dropped", "x\x1b[>0cy", "xy"},
		{"DA3 tertiary dropped", "x\x1b[=cy", "xy"},
		{"DSR status dropped", "a\x1b[5nb", "ab"},
		{"DSR cursor dropped", "a\x1b[6nb", "ab"},
		{"DECXCPR dropped", "a\x1b[?6nb", "ab"},
		{"DECRQM dropped", "a\x1b[?25$pb", "ab"},
		{"DECSTR kept (has ! not $)", "a\x1b[!pb", "a\x1b[!pb"},
		{"XTWINOPS report size dropped", "a\x1b[14tb", "ab"},
		{"XTWINOPS report chars dropped", "a\x1b[18tb", "ab"},
		{"XTWINOPS iconify kept", "a\x1b[2tb", "a\x1b[2tb"},
		{"XTWINOPS resize kept", "a\x1b[8;24;80tb", "a\x1b[8;24;80tb"},
		{"OSC 10 fg color query dropped (ST)", "a\x1b]10;?\x1b\\b", "ab"},
		{"OSC 11 bg color query dropped (BEL)", "a\x1b]11;?\x07b", "ab"},
		{"OSC 12 cursor color query dropped", "a\x1b]12;?\x07b", "ab"},
		{"OSC 4 palette query dropped", "a\x1b]4;1;?\x07b", "ab"},
		{"OSC 52 clipboard read dropped", "a\x1b]52;c;?\x07b", "ab"},
		{"OSC 0 title with ? kept", "a\x1b]0;hello?\x07b", "a\x1b]0;hello?\x07b"},
		{"OSC 0 title without ? kept", "a\x1b]0;my title\x07b", "a\x1b]0;my title\x07b"},
		{"OSC 11 set bg kept", "a\x1b]11;rgb:00/00/00\x07b", "a\x1b]11;rgb:00/00/00\x07b"},
		{"DCS DECRQSS dropped", "a\x1bP$qr\x1b\\b", "ab"},
		{"DCS sixel (non-query) kept", "a\x1bPq#0\x1b\\b", "a\x1bPq#0\x1b\\b"},
		{
			"real-world bug: DA + OSC 11 + DA",
			"prompt\x1b[c\x1b]11;?\x1b\\\x1b[c$ ",
			"prompt$ ",
		},
		{"lone ESC at end kept", "abc\x1b", "abc\x1b"},
		{"truncated CSI kept", "abc\x1b[12", "abc\x1b[12"},
		{"unterminated OSC kept", "abc\x1b]11;?", "abc\x1b]11;?"},
		{"empty input", "", ""},
		// Narrowed CSI n: queries dropped, replies passed through.
		{"CSI 0n (DSR reply) kept", "a\x1b[0nb", "a\x1b[0nb"},
		{"CSI 3n (printer reply) kept", "a\x1b[3nb", "a\x1b[3nb"},
		// 8-bit C1 introducers — xterm.js parses these in UTF-8 mode.
		{"8-bit CSI DA dropped", "a\x9bcb", "ab"},
		{"8-bit CSI DSR cursor dropped", "a\x9b6nb", "ab"},
		{"8-bit CSI SGR kept", "a\x9b31mredb", "a\x9b31mredb"},
		{"8-bit OSC 11 query (BEL) dropped", "a\x9d11;?\x07b", "ab"},
		{"8-bit OSC 11 query (8-bit ST) dropped", "a\x9d11;?\x9cb", "ab"},
		{"8-bit OSC title kept", "a\x9d0;title\x07b", "a\x9d0;title\x07b"},
		{"8-bit DCS DECRQSS dropped", "a\x90$qr\x9cb", "ab"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterReplayQueries([]byte(tc.in))
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Errorf("filterReplayQueries(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilterPreservesNonQueryBytes(t *testing.T) {
	// Stress: mix of color, cursor, text, and queries. Non-query bytes must
	// survive verbatim.
	in := []byte("\x1b[?2004h\x1b[1;31mERROR\x1b[0m: thing\n\x1b[cdone\x1b]11;?\x07")
	got := filterReplayQueries(in)
	want := []byte("\x1b[?2004h\x1b[1;31mERROR\x1b[0m: thing\ndone")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}
