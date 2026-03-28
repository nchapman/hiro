//go:build online

package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

// --- LoCoMo dataset types ---

type locomoConversation struct {
	QA           []locomoQA             `json:"qa"`
	Conversation map[string]interface{} `json:"conversation"`
}

type locomoQA struct {
	Question          string      `json:"question"`
	Answer            interface{} `json:"answer"` // string or number
	Evidence          []string    `json:"evidence"`
	Category          int         `json:"category"`
	AdversarialAnswer string      `json:"adversarial_answer,omitempty"`
}

func (q locomoQA) AnswerString() string {
	switch v := q.Answer.(type) {
	case string:
		return v
	case float64:
		if v == float64(int(v)) {
			return fmt.Sprintf("%d", int(v))
		}
		return fmt.Sprintf("%g", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

var categoryNames = map[int]string{
	1: "factual",
	2: "temporal",
	3: "inference",
	4: "world_knowledge",
	5: "adversarial",
}

// --- Dataset loading ---

const locomoURL = "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json"

func loadLoCoMo(t *testing.T) []locomoConversation {
	t.Helper()
	path := filepath.Join("testdata", "locomo10.json")

	// Auto-download if missing.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Logf("Downloading LoCoMo dataset from %s", locomoURL)
		os.MkdirAll("testdata", 0o755)
		resp, err := http.Get(locomoURL)
		if err != nil {
			t.Fatalf("downloading locomo10.json: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("downloading locomo10.json: HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading locomo10.json download: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("writing locomo10.json: %v", err)
		}
		t.Logf("Downloaded %d bytes", len(data))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading locomo10.json: %v", err)
	}
	var convos []locomoConversation
	if err := json.Unmarshal(data, &convos); err != nil {
		t.Fatalf("parsing locomo10.json: %v", err)
	}
	return convos
}

// flattenConversation extracts all dialog turns in chronological order.
type dialogTurn struct {
	Speaker   string
	Text      string
	SessionID int
	Timestamp time.Time
}

func flattenConversation(conv locomoConversation) []dialogTurn {
	convo := conv.Conversation
	speakerA, _ := convo["speaker_a"].(string)
	speakerB, _ := convo["speaker_b"].(string)

	var turns []dialogTurn

	// Find all sessions by looking for session_N keys.
	for i := 1; i <= 50; i++ {
		sessionKey := fmt.Sprintf("session_%d", i)
		dateKey := fmt.Sprintf("session_%d_date_time", i)

		sessionData, ok := convo[sessionKey]
		if !ok {
			continue
		}

		// Parse session date. LoCoMo uses "1:56 pm on 8 May, 2023" format.
		var sessionTime time.Time
		if dateStr, ok := convo[dateKey].(string); ok {
			sessionTime = parseLoCoMoDate(dateStr)
		}

		// Parse turns in this session.
		sessionArr, ok := sessionData.([]interface{})
		if !ok {
			continue
		}

		for turnIdx, turnRaw := range sessionArr {
			turnMap, ok := turnRaw.(map[string]interface{})
			if !ok {
				continue
			}
			speaker, _ := turnMap["speaker"].(string)
			text, _ := turnMap["text"].(string)
			if text == "" {
				continue
			}

			// Offset each turn within the session by 1 minute for ordering.
			turnTime := sessionTime.Add(time.Duration(turnIdx) * time.Minute)

			// Map speaker names to roles for clarity.
			role := speaker
			if speaker == speakerA {
				role = speakerA
			} else if speaker == speakerB {
				role = speakerB
			}

			turns = append(turns, dialogTurn{
				Speaker:   role,
				Text:      text,
				SessionID: i,
				Timestamp: turnTime,
			})
		}
	}
	return turns
}

// --- F1 scoring (partial match, as per LoCoMo paper) ---

func tokenize(s string) []string {
	s = strings.ToLower(s)
	// Remove punctuation.
	var cleaned strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			cleaned.WriteRune(r)
		} else {
			cleaned.WriteRune(' ')
		}
	}
	fields := strings.Fields(cleaned.String())
	// Remove stop words.
	stop := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "shall": true, "can": true,
		"of": true, "in": true, "to": true, "for": true, "with": true,
		"on": true, "at": true, "by": true, "from": true, "as": true,
		"it": true, "its": true, "this": true, "that": true,
		"and": true, "or": true, "but": true, "not": true,
		"i": true, "me": true, "my": true, "we": true, "our": true,
		"you": true, "your": true, "he": true, "she": true, "they": true,
		"his": true, "her": true, "their": true,
	}
	var result []string
	for _, w := range fields {
		if !stop[w] && w != "" {
			result = append(result, w)
		}
	}
	return result
}

func f1Score(prediction, reference string) float64 {
	predTokens := tokenize(prediction)
	refTokens := tokenize(reference)

	if len(predTokens) == 0 && len(refTokens) == 0 {
		return 1.0
	}
	if len(predTokens) == 0 || len(refTokens) == 0 {
		return 0.0
	}

	refSet := make(map[string]int)
	for _, t := range refTokens {
		refSet[t]++
	}

	common := 0
	for _, t := range predTokens {
		if refSet[t] > 0 {
			common++
			refSet[t]--
		}
	}

	if common == 0 {
		return 0.0
	}

	precision := float64(common) / float64(len(predTokens))
	recall := float64(common) / float64(len(refTokens))
	return 2 * precision * recall / (precision + recall)
}

// --- Eval runner ---

type evalResult struct {
	ConvIndex int
	Category  int
	Question  string
	Expected  string
	Got       string
	F1        float64
}

type evalSummary struct {
	ByCategory map[int][]float64
	Overall    []float64
}

func (s evalSummary) Report() string {
	var b strings.Builder

	cats := make([]int, 0, len(s.ByCategory))
	for c := range s.ByCategory {
		cats = append(cats, c)
	}
	sort.Ints(cats)

	for _, c := range cats {
		scores := s.ByCategory[c]
		avg := mean(scores)
		name := categoryNames[c]
		fmt.Fprintf(&b, "  Category %d (%s): %.1f%% F1 (%d questions)\n", c, name, avg*100, len(scores))
	}
	fmt.Fprintf(&b, "  Overall: %.1f%% F1 (%d questions)\n", mean(s.Overall)*100, len(s.Overall))
	return b.String()
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// runEval runs the LoCoMo QA evaluation.
//
// For each conversation:
//  1. Flatten dialog turns into messages and store in the platform DB
//  2. If compact=true, run compaction to compress the conversation
//  3. Assemble context from the (possibly compacted) DB
//  4. For each QA pair, ask the LLM the question with the assembled context
//  5. Score the answer against ground truth using F1 partial match
//
// compactionLevel defines a named compression configuration for multi-level eval.
type compactionLevel struct {
	Name             string
	LeafChunkTokens  int
	LeafTargetTokens int
	CondenseTarget   int
}

// maxConvos limits the number of conversations to evaluate (0 = all).
// maxQAPerConvo limits QA pairs per conversation (0 = all).
// compactCfg, when non-nil, specifies the compaction configuration to use.
// When nil, no compaction is performed (baseline).
func runEval(t *testing.T, lm fantasy.LanguageModel, compactCfg *CompactionConfig, maxConvos, maxQAPerConvo int) evalSummary {
	t.Helper()
	convos := loadLoCoMo(t)

	if maxConvos > 0 && maxConvos < len(convos) {
		convos = convos[:maxConvos]
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	summary := evalSummary{
		ByCategory: make(map[int][]float64),
	}

	for ci, conv := range convos {
		turns := flattenConversation(conv)
		if len(turns) == 0 {
			continue
		}

		pdb := openTestDB(t)
		sessionID := fmt.Sprintf("eval-%d", ci)
		createTestSession(t, pdb, sessionID)

		// Ingest all dialog turns as messages with correct timestamps.
		// The LoCoMo dataset has session-level dates; we space turns within
		// a session by 1 minute for ordering.
		for _, turn := range turns {
			content := fmt.Sprintf("[%s] %s", turn.Speaker, turn.Text)
			tokens := EstimateTokens(content)
			msgID, err := pdb.AppendMessage(sessionID, "user", content, "{}", tokens)
			if err != nil {
				t.Fatalf("AppendMessage: %v", err)
			}
			// Set the correct timestamp from the LoCoMo session date so that
			// the compaction summarizer can resolve relative time references.
			if !turn.Timestamp.IsZero() {
				pdb.UpdateMessageTimestamp(msgID, turn.Timestamp)
			}
		}

		// Optionally compact. We run multiple rounds to simulate aggressive
		// compaction like a long-running session would experience, driving
		// the context through leaf passes and condensation.
		if compactCfg != nil {
			cfg := *compactCfg

			summarizer := &lmSummarizer{lm: lm}
			preTokens, _ := pdb.ContextTokenCount(sessionID)
			estimated := preTokens

			// Run compaction repeatedly until stable (simulating multiple turns
			// of an active session). Each round may create new leaf summaries
			// or condense existing ones into higher-depth nodes.
			const maxRounds = 5
			for round := 0; round < maxRounds; round++ {
				compactor := NewCompactor(pdb, sessionID, summarizer, cfg, logger)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				_, err := compactor.CompactIfNeeded(ctx, int64(estimated))
				cancel()
				if err != nil {
					t.Logf("Conv %d round %d: compaction failed: %v", ci, round, err)
					break
				}

				newEstimated, _ := pdb.ContextTokenCount(sessionID)
				if newEstimated >= estimated {
					break // no further compression possible
				}
				estimated = newEstimated
			}

			postTokens, _ := pdb.ContextTokenCount(sessionID)
			items, _ := pdb.GetContextItems(sessionID)
			msgCount, sumCount := 0, 0
			for _, item := range items {
				if item.ItemType == "message" {
					msgCount++
				} else {
					sumCount++
				}
			}
			maxDepth, _ := pdb.MaxSummaryDepth(sessionID)
			reduction := 0.0
			if preTokens > 0 {
				reduction = (1 - float64(postTokens)/float64(preTokens)) * 100
			}
			t.Logf("Conv %d: %d turns → %d messages + %d summaries (depth %d), %d → %d est tokens (%.0f%% reduction)",
				ci, len(turns), msgCount, sumCount, maxDepth, preTokens, postTokens, reduction)
		}

		// Assemble context.
		assembleCfg := DefaultCompactionConfig()
		assembled, err := Assemble(pdb, sessionID, assembleCfg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		// Build context string from assembled messages.
		var contextBuf strings.Builder
		for _, msg := range assembled.Messages {
			for _, part := range msg.Content {
				if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
					contextBuf.WriteString(tp.Text)
					contextBuf.WriteString("\n")
				}
			}
		}
		contextStr := contextBuf.String()

		// Score QA pairs.
		qaPairs := conv.QA
		if maxQAPerConvo > 0 && maxQAPerConvo < len(qaPairs) {
			qaPairs = qaPairs[:maxQAPerConvo]
		}

		for qi, qa := range qaPairs {
			expected := qa.AnswerString()

			prompt := fmt.Sprintf(
				"Based on the following conversation history, answer the question. "+
					"Give a short, direct answer (a few words or a short phrase). "+
					"The conversation may contain summaries of earlier portions — "+
					"treat summarized content as authoritative. "+
					"If the question cannot be answered from the conversation, say \"unanswerable\".\n\n"+
					"Conversation:\n%s\n\nQuestion: %s\nAnswer:",
				contextStr, qa.Question,
			)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			resp, err := lm.Generate(ctx, fantasy.Call{
				Prompt: fantasy.Prompt{fantasy.NewUserMessage(prompt)},
			})
			cancel()

			answer := ""
			if err != nil {
				t.Logf("  [%d/%d] ERROR %q: %v", qi+1, len(qaPairs), qa.Question, err)
			} else {
				answer = strings.TrimSpace(resp.Content.Text())
			}

			score := f1Score(answer, expected)
			summary.ByCategory[qa.Category] = append(summary.ByCategory[qa.Category], score)
			summary.Overall = append(summary.Overall, score)

			// Progress: show every question with score and running average.
			catName := categoryNames[qa.Category]
			marker := "✓"
			if score < 0.5 {
				marker = "✗"
			}
			t.Logf("  [%d/%d] %s F1=%.0f%% (%s) %q → got %q (expected %q)",
				qi+1, len(qaPairs), marker, score*100, catName,
				qa.Question, truncateStr(answer, 60), truncateStr(expected, 60))
		}

		// Per-conversation running totals.
		t.Logf("Conv %d done: %d QA pairs, running overall F1=%.1f%%",
			ci, len(qaPairs), mean(summary.Overall)*100)
	}

	return summary
}

// --- Test entrypoints ---

// TestLoCoMo_Baseline runs the QA eval without compaction (full context).
// This establishes the upper bound — how well the LLM performs when it
// can see the entire conversation.
func TestLoCoMo_Baseline(t *testing.T) {
	lm := testLM(t)

	// Run on first 2 conversations, 20 QA per convo for a quick smoke test.
	summary := runEval(t, lm, nil, 2, 20)
	t.Logf("\n=== BASELINE (no compaction) ===\n%s", summary.Report())
}

// defaultEvalCompactionConfig returns the standard compaction config for evals.
func defaultEvalCompactionConfig() *CompactionConfig {
	return &CompactionConfig{
		ContextWindow:        200_000,
		SoftThreshold:        0.01, // force compaction
		HardThreshold:        0.85,
		TokenBudget:          180_000,
		FreshTailCount:       4,
		LeafChunkTokens:      4_000, // grab meaningful chunks for real compression
		LeafTargetTokens:     800,   // ~5:1 compression target at leaf level
		CondenseTargetTokens: 1_500,
		LeafMinFanout:        3,
		CondenseMinFanout:    3,
	}
}

// TestLoCoMo_Compacted runs the QA eval after compaction.
// This measures how much information survives the compaction pipeline.
func TestLoCoMo_Compacted(t *testing.T) {
	lm := testLM(t)

	// Run on first 2 conversations, 20 QA per convo for a quick smoke test.
	summary := runEval(t, lm, defaultEvalCompactionConfig(), 2, 20)
	t.Logf("\n=== COMPACTED ===\n%s", summary.Report())
}

// TestLoCoMo_Full runs the complete evaluation on all conversations.
// This is slow (hundreds of LLM calls) — run explicitly with:
//
//	go test -tags=online -run TestLoCoMo_Full -v -count=1 -timeout=60m
func TestLoCoMo_Full(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full LoCoMo eval in short mode")
	}
	lm := testLM(t)

	t.Log("=== Running baseline (no compaction) ===")
	baseline := runEval(t, lm, nil, 0, 0)
	t.Logf("\n=== BASELINE RESULTS ===\n%s", baseline.Report())

	t.Log("=== Running compacted ===")
	compacted := runEval(t, lm, defaultEvalCompactionConfig(), 0, 0)
	t.Logf("\n=== COMPACTED RESULTS ===\n%s", compacted.Report())

	// Report delta.
	t.Log("\n=== DELTA (compacted - baseline) ===")
	reportDelta(t, baseline, compacted)
}

// TestLoCoMo_CompressionLevels runs the eval at multiple compression levels
// to find where the accuracy/compression tradeoff breaks down.
//
//	go test -tags=online -run TestLoCoMo_CompressionLevels -v -timeout=120m
func TestLoCoMo_CompressionLevels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compression levels eval in short mode")
	}
	lm := testLM(t)

	levels := []compactionLevel{
		{"light", 2_000, 1_000, 1_500},
		{"medium", 4_000, 800, 1_200},
		{"heavy", 8_000, 800, 1_000},
		{"extreme", 16_000, 600, 800},
	}

	// Use 3 conversations, all QA pairs for meaningful signal.
	maxConvos := 3

	t.Log("=== Running baseline ===")
	baseline := runEval(t, lm, nil, maxConvos, 0)
	t.Logf("\n=== BASELINE ===\n%s", baseline.Report())

	for _, level := range levels {
		cfg := &CompactionConfig{
			ContextWindow:        200_000,
			SoftThreshold:        0.01,
			HardThreshold:        0.85,
			TokenBudget:          180_000,
			FreshTailCount:       4,
			LeafChunkTokens:      level.LeafChunkTokens,
			LeafTargetTokens:     level.LeafTargetTokens,
			CondenseTargetTokens: level.CondenseTarget,
			LeafMinFanout:        3,
			CondenseMinFanout:    3,
		}

		t.Logf("\n=== Running %s compression ===", level.Name)
		result := runEval(t, lm, cfg, maxConvos, 0)
		t.Logf("\n=== %s RESULTS ===\n%s", strings.ToUpper(level.Name), result.Report())
		reportDelta(t, baseline, result)
	}
}

func reportDelta(t *testing.T, baseline, compacted evalSummary) {
	t.Helper()
	cats := make([]int, 0, len(baseline.ByCategory))
	for c := range baseline.ByCategory {
		cats = append(cats, c)
	}
	sort.Ints(cats)
	for _, c := range cats {
		bAvg := mean(baseline.ByCategory[c])
		cAvg := mean(compacted.ByCategory[c])
		delta := (cAvg - bAvg) * 100
		t.Logf("  Category %d (%s): %+.1f%%", c, categoryNames[c], delta)
	}
	bOverall := mean(baseline.Overall)
	cOverall := mean(compacted.Overall)
	t.Logf("  Overall: %+.1f%%", (cOverall-bOverall)*100)
}

// parseLoCoMoDate parses LoCoMo's date format: "1:56 pm on 8 May, 2023".
func parseLoCoMoDate(s string) time.Time {
	// Try the primary format with "on" separator.
	layouts := []string{
		"3:04 pm on 2 January, 2006",
		"3:04 am on 2 January, 2006",
		"12:04 pm on 2 January, 2006",
		"12:04 am on 2 January, 2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// Fallback: try just the date portion after "on ".
	if idx := strings.Index(s, "on "); idx >= 0 {
		dateStr := s[idx+3:]
		if t, err := time.Parse("2 January, 2006", dateStr); err == nil {
			return t
		}
	}
	return time.Time{}
}

func truncateStr(s string, maxLen int) string {
	// Collapse to single line for log readability.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
