package inference

import (
	"encoding/json"
	"sort"

	"charm.land/fantasy"
)

// DeltaReplayType is the ProviderOptions key for delta replay metadata.
const DeltaReplayType = "hiro.delta"

func init() {
	fantasy.RegisterProviderType(DeltaReplayType, func(data []byte) (fantasy.ProviderOptionsData, error) {
		var v DeltaReplay
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	})
}

// DeltaReplay is stored in ProviderOptions on persisted context messages.
// Used to reconstruct the announced set by replaying prior deltas.
type DeltaReplay struct {
	ContextType  string   `json:"context_type"`
	AddedNames   []string `json:"added_names"`
	RemovedNames []string `json:"removed_names"`
}

// Options implements fantasy.ProviderOptionsData.
func (d *DeltaReplay) Options() {}

// MarshalJSON implements custom JSON marshaling with type info.
func (d DeltaReplay) MarshalJSON() ([]byte, error) {
	type plain DeltaReplay
	return fantasy.MarshalProviderType(DeltaReplayType, plain(d))
}

// UnmarshalJSON implements custom JSON unmarshaling with type info.
func (d *DeltaReplay) UnmarshalJSON(data []byte) error {
	type plain DeltaReplay
	var p plain
	if err := fantasy.UnmarshalProviderType(data, &p); err != nil {
		return err
	}
	*d = DeltaReplay(p)
	return nil
}

// ContextProvider computes a context delta for the current turn.
// It receives the active tool set and the assembled conversation history
// (which includes prior delta messages with DeltaReplay in ProviderOptions).
// Returns nil if nothing changed since the last announcement.
type ContextProvider func(activeTools map[string]bool, history []fantasy.Message) *DeltaResult

// DeltaResult is the output of a ContextProvider when something changed.
type DeltaResult struct {
	Message fantasy.Message // <system-reminder> user message to persist
}

// replayAnnounced scans history for DeltaReplay entries of the given type
// and replays add/remove operations to reconstruct the announced set.
func replayAnnounced(contextType string, history []fantasy.Message) map[string]bool {
	announced := make(map[string]bool)
	for _, msg := range history {
		dr := extractDeltaReplay(msg)
		if dr == nil || dr.ContextType != contextType {
			continue
		}
		for _, name := range dr.AddedNames {
			announced[name] = true
		}
		for _, name := range dr.RemovedNames {
			delete(announced, name)
		}
	}
	return announced
}

// extractDeltaReplay extracts the DeltaReplay from a message's ProviderOptions, or nil.
func extractDeltaReplay(msg fantasy.Message) *DeltaReplay {
	if msg.ProviderOptions == nil {
		return nil
	}
	dr, ok := msg.ProviderOptions[DeltaReplayType]
	if !ok {
		return nil
	}
	replay, ok := dr.(*DeltaReplay)
	if !ok {
		return nil
	}
	return replay
}

// buildDeltaMessage creates a <system-reminder> user message with DeltaReplay metadata.
func buildDeltaMessage(text, contextType string, added, removed []string) fantasy.Message {
	msg := fantasy.NewUserMessage("<system-reminder>\n" + text + "\n</system-reminder>")
	// Sort for deterministic output.
	sort.Strings(added)
	sort.Strings(removed)
	msg.ProviderOptions = fantasy.ProviderOptions{
		DeltaReplayType: &DeltaReplay{
			ContextType:  contextType,
			AddedNames:   added,
			RemovedNames: removed,
		},
	}
	return msg
}

// computeDeltas calls all providers and returns any delta messages to persist.
// Deduplicates by context type (first provider per type wins).
func computeDeltas(providers []ContextProvider, activeTools map[string]bool, history []fantasy.Message) []fantasy.Message {
	var results []fantasy.Message
	seen := make(map[string]bool)
	for _, p := range providers {
		dr := p(activeTools, history)
		if dr == nil {
			continue
		}
		// Extract context type from the delta replay for dedup.
		if replay := extractDeltaReplay(dr.Message); replay != nil {
			if seen[replay.ContextType] {
				continue
			}
			seen[replay.ContextType] = true
		}
		results = append(results, dr.Message)
	}
	return results
}
