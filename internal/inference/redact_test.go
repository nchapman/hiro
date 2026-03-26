package inference

import "testing"

func TestRedactor_NilIsNoOp(t *testing.T) {
	r := NewRedactor(nil)
	if r != nil {
		t.Fatal("expected nil redactor for nil secretsFn")
	}
	// Nil receiver should be safe to call.
	if got := r.Redact("hello secret"); got != "hello secret" {
		t.Errorf("nil redactor should pass through, got %q", got)
	}
}

func TestRedactor_ReplacesSecrets(t *testing.T) {
	r := NewRedactor(func() []string {
		return []string{"API_KEY=sk-secret-12345678", "DB_PASS=hunter2hunter2"}
	})
	got := r.Redact("got sk-secret-12345678 and hunter2hunter2 in output")
	if got != "got [API_KEY] and [DB_PASS] in output" {
		t.Errorf("unexpected redaction: %q", got)
	}
}

func TestRedactor_SkipsShortSecrets(t *testing.T) {
	r := NewRedactor(func() []string {
		return []string{"SHORT=abc"} // len 3 < minSecretLen (8)
	})
	got := r.Redact("value is abc here")
	if got != "value is abc here" {
		t.Errorf("short secrets should not be redacted: %q", got)
	}
}

func TestRedactor_LongestFirst(t *testing.T) {
	// If one secret value is a substring of another, the longer should be replaced first.
	r := NewRedactor(func() []string {
		return []string{
			"FULL=supersecretvalue123",
			"PARTIAL=secretvalue",
		}
	})
	got := r.Redact("the supersecretvalue123 is here")
	if got != "the [FULL] is here" {
		t.Errorf("expected longest-first replacement, got %q", got)
	}
}

func TestRedactor_EmptySecrets(t *testing.T) {
	r := NewRedactor(func() []string { return nil })
	got := r.Redact("nothing to redact")
	if got != "nothing to redact" {
		t.Error("empty secrets should pass through")
	}
}

func TestRedactor_MalformedPair(t *testing.T) {
	r := NewRedactor(func() []string {
		return []string{"NOEQUALS"} // no = separator
	})
	got := r.Redact("NOEQUALS in text")
	if got != "NOEQUALS in text" {
		t.Errorf("malformed pairs should be skipped: %q", got)
	}
}
