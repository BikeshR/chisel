package trust

import "testing"

func TestContentHashIsStableAndDistinguishesContent(t *testing.T) {
	a := ContentHash([]byte(`{"rules":{}}`))
	b := ContentHash([]byte(`{"rules":{}}`))
	if a != b {
		t.Error("ContentHash should be deterministic for the same content")
	}

	c := ContentHash([]byte(`{"rules":{"bash":{}}}`))
	if a == c {
		t.Error("ContentHash should differ for different content")
	}
}

func TestIsTrustedFalseByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := Open("trusted_test.json")

	trusted, err := s.IsTrusted(ContentHash([]byte("anything")))
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Error("expected untrusted content to report false by default")
	}
}

func TestTrustPersistsAcrossLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := Open("trusted_test.json")
	hash := ContentHash([]byte(`{"rules":{"bash":{"git *":"allow"}}}`))

	trusted, err := s.IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("should not be trusted before Trust is called")
	}

	if err := s.Trust(hash); err != nil {
		t.Fatalf("Trust: %v", err)
	}

	trusted, err = s.IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted after Trust: %v", err)
	}
	if !trusted {
		t.Error("expected the hash to be trusted after Trust was called")
	}
}

func TestTrustDoesNotAffectOtherContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := Open("trusted_test.json")

	trustedHash := ContentHash([]byte("trusted content"))
	otherHash := ContentHash([]byte("different content"))

	if err := s.Trust(trustedHash); err != nil {
		t.Fatal(err)
	}

	trusted, err := s.IsTrusted(otherHash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("trusting one hash should not trust a different one")
	}
}

// TestSeparateStoresDoNotShareTrust is the reason Store takes a
// filename: hooks and permission rules must not implicitly trust each
// other just because one file's content happens to be approved.
func TestSeparateStoresDoNotShareTrust(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hooksStore := Open("trusted_hooks.json")
	rulesStore := Open("trusted_permrules.json")

	hash := ContentHash([]byte("identical content in both files"))
	if err := hooksStore.Trust(hash); err != nil {
		t.Fatal(err)
	}

	trusted, err := rulesStore.IsTrusted(hash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("trusting a hash in one store must not trust it in a different store")
	}
}
