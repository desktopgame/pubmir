package tokenize

import (
	"testing"
)

func TestTokenizeStableAndLongestMatchFirst(t *testing.T) {
	// "10.0.0.1" is a substring of "110.0.0.1"-shaped values in spirit; here
	// we use a case where one secret value is literally a substring of
	// another, to prove longest-match-first avoids partial corruption.
	current := map[string]string{
		"SHORT": "example",
		"LONG":  "example-long-host",
	}
	m := NewMapper(current, nil)

	got := string(m.Tokenize([]byte("connect to example-long-host now")))
	want := "connect to " + Token("LONG") + " now"
	if got != want {
		t.Fatalf("Tokenize longest-match-first: got %q, want %q", got, want)
	}

	// Same value tokenized twice must always produce the same token.
	a := string(m.Tokenize([]byte("example-long-host")))
	b := string(m.Tokenize([]byte("example-long-host")))
	if a != b {
		t.Fatalf("Tokenize not stable: %q vs %q", a, b)
	}
}

func TestMapperIncludesHistoricalValues(t *testing.T) {
	current := map[string]string{"IP": "203.0.113.99"}
	history := map[string][]string{"IP": {"203.0.113.42", "203.0.113.99"}}
	m := NewMapper(current, history)

	got := string(m.Tokenize([]byte("old value 203.0.113.42 and new value 203.0.113.99")))
	want := "old value " + Token("IP") + " and new value " + Token("IP")
	if got != want {
		t.Fatalf("Tokenize with history: got %q, want %q", got, want)
	}
}

func TestDetokenizeRestoresCurrentValue(t *testing.T) {
	current := map[string]string{"IP": "203.0.113.99"}
	out, err := Detokenize([]byte("ssh to "+Token("IP")), current)
	if err != nil {
		t.Fatalf("Detokenize: %v", err)
	}
	if string(out) != "ssh to 203.0.113.99" {
		t.Fatalf("Detokenize got %q", out)
	}
}

func TestDetokenizeUnknownTokenErrors(t *testing.T) {
	current := map[string]string{"IP": "203.0.113.99"}
	_, err := Detokenize([]byte("ssh to "+Token("UNKNOWN")), current)
	if err == nil {
		t.Fatal("expected error for unknown token, got nil")
	}
}

func TestFindLeakingKey(t *testing.T) {
	m := NewMapper(map[string]string{"IP": "203.0.113.99"}, nil)
	if _, ok := m.FindLeakingKey("configs/203.0.113.99-backup"); !ok {
		t.Fatal("expected FindLeakingKey to detect the secret value in a path")
	}
	if _, ok := m.FindLeakingKey("configs/normal-file"); ok {
		t.Fatal("FindLeakingKey false positive on a clean path")
	}
}

func TestIsExcluded(t *testing.T) {
	patterns := []string{"secrets/**", "**/*.key"}
	cases := map[string]bool{
		"secrets/creds.txt": true,
		"a/b/id.key":        true,
		"docs/notes.txt":    false,
	}
	for path, want := range cases {
		if got := IsExcluded(path, patterns); got != want {
			t.Errorf("IsExcluded(%q) = %v, want %v", path, got, want)
		}
	}
}
