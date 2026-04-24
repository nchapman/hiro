package api

// filterReplayQueries strips terminal query escape sequences from a replay
// byte stream. Query sequences (Device Attributes, DSR, OSC color/palette
// queries, XTWINOPS size reports, DECRQSS) are what a TUI sends to ask the
// terminal for information — the terminal is expected to reply.
//
// xterm.js answers these on its own. If we replay a buffer that contains
// queries, xterm parses them out of context and emits replies via its
// onData callback. Those replies get forwarded to the live PTY's stdin,
// where they land at the shell prompt as spurious input (e.g. "1;2c" from
// a DA1 reply, "11;rgb:..." from an OSC 11 reply) if the originating TUI
// has since exited.
//
// Only the replay stream needs filtering. Live PTY output is fine because
// the TUI that emitted the query is also the one consuming the reply —
// everything stays paired. The replay is where pairing breaks.
//
// The filter is intentionally conservative: it only drops byte sequences
// that are definitely queries (known final bytes, known OSC/DCS prefixes),
// and passes everything else through untouched.
func filterReplayQueries(in []byte) []byte {
	out := make([]byte, 0, len(in))
	i := 0
	for i < len(in) {
		b := in[i]
		// 7-bit introducers: ESC [ / ESC ] / ESC P.
		if b == 0x1B && i+1 < len(in) {
			switch in[i+1] {
			case '[':
				end, drop := scanCSI(in, i+2)
				if !drop {
					out = append(out, in[i:end]...)
				}
				i = end
				continue
			case ']':
				end, drop := scanOSC(in, i+2)
				if !drop {
					out = append(out, in[i:end]...)
				}
				i = end
				continue
			case 'P':
				end, drop := scanDCS(in, i+2)
				if !drop {
					out = append(out, in[i:end]...)
				}
				i = end
				continue
			}
		}
		// 8-bit C1 introducers. xterm.js parses these in UTF-8 mode, so a TUI
		// emitting 0x9B/0x9D/0x90 would bypass the 7-bit filter otherwise.
		switch b {
		case 0x9B: // CSI
			end, drop := scanCSI(in, i+1)
			if !drop {
				out = append(out, in[i:end]...)
			}
			i = end
			continue
		case 0x9D: // OSC
			end, drop := scanOSC(in, i+1)
			if !drop {
				out = append(out, in[i:end]...)
			}
			i = end
			continue
		case 0x90: // DCS
			end, drop := scanDCS(in, i+1)
			if !drop {
				out = append(out, in[i:end]...)
			}
			i = end
			continue
		}
		out = append(out, b)
		i++
	}
	return out
}

// scanCSI parses a CSI sequence whose payload begins at in[bodyStart]
// (i.e. after the ESC [ or 0x9B introducer). Returns the index just past
// the sequence and whether it is a query to drop.
// Grammar: <params 0x30-0x3F>* <intermediates 0x20-0x2F>* <final 0x40-0x7E>
func scanCSI(in []byte, bodyStart int) (end int, drop bool) {
	i := bodyStart
	paramStart := i
	for i < len(in) && in[i] >= 0x30 && in[i] <= 0x3F {
		i++
	}
	params := in[paramStart:i]
	interStart := i
	for i < len(in) && in[i] >= 0x20 && in[i] <= 0x2F {
		i++
	}
	inter := in[interStart:i]
	if i >= len(in) {
		// Truncated — pass the partial bytes through rather than guess.
		return len(in), false
	}
	final := in[i]
	i++
	if final < 0x40 || final > 0x7E {
		// Malformed; keep it, let xterm cope.
		return i, false
	}
	return i, isCSIQuery(params, inter, final)
}

func isCSIQuery(params, inter []byte, final byte) bool {
	switch final {
	case 'c':
		// DA1/DA2/DA3: CSI c, CSI > c, CSI = c. All forms are queries.
		return true
	case 'n':
		// DSR queries: CSI 5 n (status), CSI 6 n (cursor pos), CSI ? Ps n
		// (DEC-specific reports). Replies to these also end in 'n' (e.g.
		// CSI 0 n), so restrict to known query parameter forms to avoid
		// eating a TUI's own output if it ever contains a reply-shape.
		if len(params) > 0 && params[0] == '?' {
			return true
		}
		switch string(params) {
		case "5", "6":
			return true
		}
	case 'p':
		// DECRQM: CSI <mode> $ p or CSI ? <mode> $ p.
		// Other CSI ...p sequences (DECSCL, DECSTR '!p') are not queries, so
		// gate on the '$' intermediate.
		return len(inter) == 1 && inter[0] == '$'
	case 't':
		// XTWINOPS. Params 11,13-21 are report requests; lower values are
		// commands (iconify, move, resize) that must pass through.
		switch string(params) {
		case "11", "13", "14", "15", "16", "17", "18", "19", "20", "21":
			return true
		}
	}
	return false
}

// scanOSC parses an OSC sequence whose payload begins at in[bodyStart]
// (after the ESC ] or 0x9D introducer). Payload is terminated by BEL
// (0x07), 7-bit ST (ESC \), or 8-bit ST (0x9C).
func scanOSC(in []byte, bodyStart int) (end int, drop bool) {
	i := bodyStart
	for i < len(in) {
		b := in[i]
		if b == 0x07 || b == 0x9C {
			return i + 1, isOSCQuery(in[bodyStart:i])
		}
		if b == 0x1B && i+1 < len(in) && in[i+1] == '\\' {
			return i + 2, isOSCQuery(in[bodyStart:i])
		}
		i++
	}
	// Unterminated — pass through.
	return len(in), false
}

// isOSCQuery reports whether an OSC payload is a color/palette/clipboard query.
// Query payloads are of the form "<Ps>;...;?" — the trailing ";?" is the
// distinctive marker. Requires the '?' to be preceded by ';' to avoid false
// positives on legitimate payloads that happen to end in '?' (e.g. a window
// title "hello?"). OSC 52 is a special case (";?" may be followed by another
// ";" for the selection parameter).
func isOSCQuery(payload []byte) bool {
	for i := 1; i < len(payload); i++ {
		if payload[i] != '?' || payload[i-1] != ';' {
			continue
		}
		if i == len(payload)-1 || payload[i+1] == ';' {
			return true
		}
	}
	return false
}

// scanDCS parses a DCS sequence whose payload begins at in[bodyStart]
// (after the ESC P or 0x90 introducer). Terminated by 7-bit or 8-bit ST.
func scanDCS(in []byte, bodyStart int) (end int, drop bool) {
	i := bodyStart
	for i < len(in) {
		if in[i] == 0x9C {
			return i + 1, isDCSQuery(in[bodyStart:i])
		}
		if in[i] == 0x1B && i+1 < len(in) && in[i+1] == '\\' {
			return i + 2, isDCSQuery(in[bodyStart:i])
		}
		i++
	}
	return len(in), false
}

// isDCSQuery reports whether a DCS payload is a DECRQSS request (prefix "$q").
func isDCSQuery(payload []byte) bool {
	return len(payload) >= 2 && payload[0] == '$' && payload[1] == 'q'
}
