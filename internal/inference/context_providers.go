package inference

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/fantasy"

	"github.com/nchapman/hiro/internal/config"
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

// DeltaEntry holds replay data for a single context type.
// For named-set providers, AddedNames/RemovedNames track changes.
// For blob providers, ContentHash detects changes via content hashing.
type DeltaEntry struct {
	ContextType  string   `json:"context_type"`
	AddedNames   []string `json:"added_names,omitempty"`
	RemovedNames []string `json:"removed_names,omitempty"`
	ContentHash  string   `json:"content_hash,omitempty"`
}

// DeltaReplay is stored in ProviderOptions on persisted context messages.
// Contains one or more entries (multiple when several providers emit in
// the same turn, producing a single merged message).
type DeltaReplay struct {
	Entries []DeltaEntry `json:"entries"`
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
		if dr == nil {
			continue
		}
		for _, e := range dr.Entries {
			if e.ContextType != contextType {
				continue
			}
			for _, name := range e.AddedNames {
				announced[name] = true
			}
			for _, name := range e.RemovedNames {
				delete(announced, name)
			}
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

// buildDeltaMessage creates a <system-reminder> user message with a single DeltaEntry.
func buildDeltaMessage(text, contextType string, added, removed []string) fantasy.Message {
	msg := fantasy.NewUserMessage("<system-reminder>\n" + text + "\n</system-reminder>")
	// Sort for deterministic output.
	sort.Strings(added)
	sort.Strings(removed)
	msg.ProviderOptions = fantasy.ProviderOptions{
		DeltaReplayType: &DeltaReplay{
			Entries: []DeltaEntry{{
				ContextType:  contextType,
				AddedNames:   added,
				RemovedNames: removed,
			}},
		},
	}
	return msg
}

// computeDeltas calls all providers and merges results into a single
// <system-reminder> message. Deduplicates by context type (first provider
// per type wins). Returns nil if no providers emitted.
func computeDeltas(providers []ContextProvider, activeTools map[string]bool, history []fantasy.Message) []fantasy.Message {
	var texts []string
	var entries []DeltaEntry
	seen := make(map[string]bool)

	for _, p := range providers {
		dr := p(activeTools, history)
		if dr == nil {
			continue
		}
		replay := extractDeltaReplay(dr.Message)
		if replay == nil {
			continue
		}
		// Dedup by context type.
		skip := false
		for _, e := range replay.Entries {
			if seen[e.ContextType] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, e := range replay.Entries {
			seen[e.ContextType] = true
		}
		// Extract the text content (without system-reminder wrapper).
		tp, _ := dr.Message.Content[0].(fantasy.TextPart)
		text := tp.Text
		text = strings.TrimPrefix(text, "<system-reminder>\n")
		text = strings.TrimSuffix(text, "\n</system-reminder>")
		texts = append(texts, text)
		entries = append(entries, replay.Entries...)
	}

	if len(entries) == 0 {
		return nil
	}

	merged := fantasy.NewUserMessage("<system-reminder>\n" + strings.Join(texts, "\n\n") + "\n</system-reminder>")
	merged.ProviderOptions = fantasy.ProviderOptions{
		DeltaReplayType: &DeltaReplay{Entries: entries},
	}
	return []fantasy.Message{merged}
}

// --- Content-hash helpers for blob providers ---

// replayLatestHash finds the most recent content hash for a context type in history.
func replayLatestHash(contextType string, history []fantasy.Message) string {
	var latest string
	for _, msg := range history {
		dr := extractDeltaReplay(msg)
		if dr == nil {
			continue
		}
		for _, e := range dr.Entries {
			if e.ContextType == contextType && e.ContentHash != "" {
				latest = e.ContentHash
			}
		}
	}
	return latest
}

// contentHash returns a short SHA-256 hex digest (16 chars) of s.
func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// buildContentMessage creates a <system-reminder> message with a single content-hash entry.
func buildContentMessage(text, contextType, hash string) fantasy.Message {
	msg := fantasy.NewUserMessage("<system-reminder>\n" + text + "\n</system-reminder>")
	msg.ProviderOptions = fantasy.ProviderOptions{
		DeltaReplayType: &DeltaReplay{
			Entries: []DeltaEntry{{
				ContextType: contextType,
				ContentHash: hash,
			}},
		},
	}
	return msg
}

// --- Skill provider (named set, like agents) ---

// SkillProvider returns a ContextProvider that announces available skills.
// Only emits when the skill set changes compared to prior announcements.
func SkillProvider(agentDefDir, sharedSkillDir string) ContextProvider {
	return func(activeTools map[string]bool, history []fantasy.Message) *DeltaResult {
		if !activeTools["Skill"] {
			return nil
		}

		// Load current skills from disk.
		agentSkills, _ := config.LoadSkills(filepath.Join(agentDefDir, "skills"))
		sharedSkills, _ := config.LoadSkills(sharedSkillDir)
		skills := config.MergeSkills(agentSkills, sharedSkills)

		current := make(map[string]bool, len(skills))
		for _, s := range skills {
			current[s.Name] = true
		}

		announced := replayAnnounced("skills", history)

		var added, removed []string
		for name := range current {
			if !announced[name] {
				added = append(added, name)
			}
		}
		for name := range announced {
			if !current[name] {
				removed = append(removed, name)
			}
		}

		if len(added) == 0 && len(removed) == 0 {
			return nil
		}

		isInitial := len(announced) == 0
		text := renderSkillListing(skills, added, removed, isInitial)

		return &DeltaResult{
			Message: buildDeltaMessage(text, "skills", added, removed),
		}
	}
}

// renderSkillListing produces human-readable text for the skill listing delta.
func renderSkillListing(skills []config.SkillConfig, added, removed []string, isInitial bool) string {
	var b strings.Builder
	if isInitial {
		b.WriteString("## Skills\n\nDescriptions are triggers, not instructions. Call Skill to get full instructions.\n\n")
		for _, s := range skills {
			fmt.Fprintf(&b, "- **%s**: %s\n", s.Name, s.Description)
		}
		return b.String()
	}
	if len(added) > 0 {
		b.WriteString("New skills available:\n\n")
		addedSet := make(map[string]bool, len(added))
		for _, name := range added {
			addedSet[name] = true
		}
		for _, s := range skills {
			if addedSet[s.Name] {
				fmt.Fprintf(&b, "- **%s**: %s\n", s.Name, s.Description)
			}
		}
	}
	if len(removed) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		sorted := make([]string, len(removed))
		copy(sorted, removed)
		sort.Strings(sorted)
		b.WriteString("Skills no longer available:\n")
		for _, name := range sorted {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	return b.String()
}

// --- Secret provider (named set) ---

// SecretProvider returns a ContextProvider that announces available secret names.
// Only emits when the secret set changes.
func SecretProvider(secretNamesFn func() []string) ContextProvider {
	return func(activeTools map[string]bool, history []fantasy.Message) *DeltaResult {
		if secretNamesFn == nil {
			return nil
		}
		names := secretNamesFn()

		current := make(map[string]bool, len(names))
		for _, n := range names {
			current[n] = true
		}

		announced := replayAnnounced("secrets", history)

		var added, removed []string
		for name := range current {
			if !announced[name] {
				added = append(added, name)
			}
		}
		for name := range announced {
			if !current[name] {
				removed = append(removed, name)
			}
		}

		if len(added) == 0 && len(removed) == 0 {
			return nil
		}

		isInitial := len(announced) == 0
		text := renderSecretListing(names, added, removed, isInitial)

		return &DeltaResult{
			Message: buildDeltaMessage(text, "secrets", added, removed),
		}
	}
}

// renderSecretListing produces human-readable text for the secret listing delta.
func renderSecretListing(allNames, added, removed []string, isInitial bool) string {
	var b strings.Builder
	if isInitial {
		b.WriteString("## Secrets\n\nAvailable as env vars in bash only. Never expose values.\n\n")
		for _, name := range allNames {
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
		return b.String()
	}
	if len(added) > 0 {
		b.WriteString("New secrets available:\n\n")
		sorted := make([]string, len(added))
		copy(sorted, added)
		sort.Strings(sorted)
		for _, name := range sorted {
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
	}
	if len(removed) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		sorted := make([]string, len(removed))
		copy(sorted, removed)
		sort.Strings(sorted)
		b.WriteString("Secrets removed:\n")
		for _, name := range sorted {
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
	}
	return b.String()
}

// --- Memory provider (content hash) ---

// MemoryProvider returns a ContextProvider that emits memory content
// when it changes (detected via content hash).
func MemoryProvider(instanceDir string) ContextProvider {
	if instanceDir == "" {
		return func(_ map[string]bool, _ []fantasy.Message) *DeltaResult { return nil }
	}
	return func(_ map[string]bool, history []fantasy.Message) *DeltaResult {
		content, err := config.ReadMemoryFile(instanceDir)
		if err != nil || content == "" {
			return nil
		}

		hash := contentHash(content)
		if replayLatestHash("memory", history) == hash {
			return nil
		}

		text := "## Memories\n\n" + content
		return &DeltaResult{
			Message: buildContentMessage(text, "memory", hash),
		}
	}
}

// --- Todo provider (content hash) ---

// TodoProvider returns a ContextProvider that emits todo content
// when it changes (detected via content hash).
func TodoProvider(sessionDir string) ContextProvider {
	if sessionDir == "" {
		return func(_ map[string]bool, _ []fantasy.Message) *DeltaResult { return nil }
	}
	return func(_ map[string]bool, history []fantasy.Message) *DeltaResult {
		todos, err := config.ReadTodos(sessionDir)
		if err != nil {
			return nil
		}
		formatted := config.FormatTodos(todos)
		if formatted == "" {
			return nil
		}

		hash := contentHash(formatted)
		if replayLatestHash("todos", history) == hash {
			return nil
		}

		text := "## Current Tasks\n\n" + formatted
		return &DeltaResult{
			Message: buildContentMessage(text, "todos", hash),
		}
	}
}
