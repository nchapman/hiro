package models

import "testing"

func TestLookup_KnownModel(t *testing.T) {
	m, ok := Lookup("claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("expected claude-sonnet-4-20250514 to be found")
	}
	if m.ContextWindow <= 0 {
		t.Error("expected positive context window")
	}
	if m.CostPer1MIn <= 0 {
		t.Error("expected positive input cost")
	}
	if m.CostPer1MOut <= 0 {
		t.Error("expected positive output cost")
	}
}

func TestLookup_UnknownModel(t *testing.T) {
	_, ok := Lookup("totally-fake-model-xyz")
	if ok {
		t.Error("expected unknown model to return false")
	}
}

func TestLookup_SpecFormat(t *testing.T) {
	// "provider/model" spec strings should resolve by stripping the provider prefix.
	m, ok := Lookup("anthropic/claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("expected spec format 'anthropic/claude-sonnet-4-20250514' to resolve")
	}
	if m.ContextWindow <= 0 {
		t.Error("expected positive context window")
	}

	// Unknown provider prefix should still work if the model part matches.
	m2, ok := Lookup("custom-provider/claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("expected spec with unknown provider to resolve via model part")
	}
	if m2.ID != m.ID {
		t.Errorf("expected same model, got %q vs %q", m2.ID, m.ID)
	}

	// Three-component spec (OpenRouter models have slashes in their IDs).
	// "openrouter/x-ai/grok-3-mini-beta" → strip "openrouter" → "x-ai/grok-3-mini-beta".
	orModels := ModelsForProvider("openrouter")
	if len(orModels) > 0 {
		raw := orModels[0].ID // e.g. "x-ai/grok-3-mini-beta"
		spec := "openrouter/" + raw
		m3, ok := Lookup(spec)
		if !ok {
			t.Fatalf("expected 3-component spec %q to resolve", spec)
		}
		if m3.ID != raw {
			t.Errorf("expected model ID %q, got %q", raw, m3.ID)
		}
	}
}

func TestContextWindow_KnownModel(t *testing.T) {
	cw := ContextWindow("claude-sonnet-4-20250514")
	if cw < 100_000 {
		t.Errorf("expected large context window, got %d", cw)
	}
}

func TestContextWindow_UnknownModel(t *testing.T) {
	cw := ContextWindow("totally-fake-model-xyz")
	if cw != DefaultContextWindow {
		t.Errorf("expected default %d, got %d", DefaultContextWindow, cw)
	}
}

func TestCost_KnownModel(t *testing.T) {
	cost := Cost("claude-sonnet-4-20250514", 1_000_000, 500_000, 0, 0)
	if cost <= 0 {
		t.Error("expected positive cost for known model")
	}
}

func TestCost_UnknownModel(t *testing.T) {
	cost := Cost("totally-fake-model-xyz", 1_000_000, 500_000, 0, 0)
	if cost != 0 {
		t.Error("expected zero cost for unknown model")
	}
}

func TestModelsForProvider_Anthropic(t *testing.T) {
	models := ModelsForProvider("anthropic")
	if len(models) == 0 {
		t.Fatal("expected anthropic models")
	}
	// Should include a known model.
	found := false
	for _, m := range models {
		if m.ID == "claude-sonnet-4-20250514" {
			found = true
			if m.ContextWindow <= 0 {
				t.Error("expected positive context window")
			}
			if !m.CanReason {
				t.Error("expected CanReason for sonnet-4")
			}
			break
		}
	}
	if !found {
		t.Error("expected to find claude-sonnet-4-20250514 in anthropic models")
	}
}

func TestModelsForProvider_Unknown(t *testing.T) {
	models := ModelsForProvider("totally-fake-provider")
	if len(models) != 0 {
		t.Errorf("expected 0 models for fake provider, got %d", len(models))
	}
}

func TestModelsForProvider_HasReasoningLevels(t *testing.T) {
	models := ModelsForProvider("anthropic")
	for _, m := range models {
		if m.CanReason && len(m.ReasoningLevels) > 0 {
			// At least one model should have reasoning levels.
			return
		}
	}
	t.Error("expected at least one model with reasoning levels")
}

func TestModelsForProviders_Multiple(t *testing.T) {
	models := ModelsForProviders([]string{"anthropic", "openrouter"})
	anthropic := ModelsForProvider("anthropic")
	openrouter := ModelsForProvider("openrouter")

	if len(models) != len(anthropic)+len(openrouter) {
		t.Errorf("expected %d models, got %d", len(anthropic)+len(openrouter), len(models))
	}
}

func TestModelsForProviders_Empty(t *testing.T) {
	models := ModelsForProviders(nil)
	if len(models) != 0 {
		t.Errorf("expected 0 models for nil providers, got %d", len(models))
	}

	models = ModelsForProviders([]string{})
	if len(models) != 0 {
		t.Errorf("expected 0 models for empty providers, got %d", len(models))
	}
}

func TestModelsForProviders_UnknownProvider(t *testing.T) {
	models := ModelsForProviders([]string{"anthropic", "totally-fake"})
	anthropic := ModelsForProvider("anthropic")

	if len(models) != len(anthropic) {
		t.Errorf("expected %d models (fake provider contributes 0), got %d", len(anthropic), len(models))
	}
}

func TestRegistryPopulated(t *testing.T) {
	ensureInit()
	if len(registry) < 10 {
		t.Errorf("expected at least 10 models in registry, got %d", len(registry))
	}
}
