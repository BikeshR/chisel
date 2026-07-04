package hooks

import (
	"testing"
)

func TestContentHashIsStableAndDistinguishesContent(t *testing.T) {
	a := ContentHash([]byte(`{"hooks":{}}`))
	b := ContentHash([]byte(`{"hooks":{}}`))
	if a != b {
		t.Error("ContentHash should be deterministic for the same content")
	}

	c := ContentHash([]byte(`{"hooks":{"preToolUse":[]}}`))
	if a == c {
		t.Error("ContentHash should differ for different content")
	}
}

func TestIsTrustedFalseByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	trusted, err := IsTrusted(ContentHash([]byte("anything")))
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Error("expected untrusted content to report false by default")
	}
}

func TestTrustPersistsAcrossLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	hash := ContentHash([]byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`))

	trusted, err := IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("should not be trusted before Trust is called")
	}

	if err := Trust(hash); err != nil {
		t.Fatalf("Trust: %v", err)
	}

	trusted, err = IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted after Trust: %v", err)
	}
	if !trusted {
		t.Error("expected the hash to be trusted after Trust was called")
	}
}

func TestTrustDoesNotAffectOtherContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	trustedHash := ContentHash([]byte("trusted content"))
	otherHash := ContentHash([]byte("different content"))

	if err := Trust(trustedHash); err != nil {
		t.Fatal(err)
	}

	trusted, err := IsTrusted(otherHash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("trusting one hash should not trust a different one — any change to hooks.json needs its own approval")
	}
}

func TestConfigHasAny(t *testing.T) {
	var empty Config
	if empty.HasAny() {
		t.Error("HasAny() = true for an empty config")
	}

	var withPre Config
	withPre.Hooks.PreToolUse = []Hook{{Match: "*", Command: "exit 0"}}
	if !withPre.HasAny() {
		t.Error("HasAny() = false with a preToolUse hook configured")
	}

	var withPost Config
	withPost.Hooks.PostToolUse = []Hook{{Match: "*", Command: "exit 0"}}
	if !withPost.HasAny() {
		t.Error("HasAny() = false with a postToolUse hook configured")
	}
}
