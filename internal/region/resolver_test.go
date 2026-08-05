package region

import "testing"

func TestResolverStableAndConfigured(t *testing.T) {
	resolver := NewResolver("v2", []string{"west", "east", "north"})
	one := resolver.Resolve("snaplink", "user-42")
	if one == "" {
		t.Fatal("Resolve returned an empty region")
	}
	if again := resolver.Resolve("snaplink", "user-42"); again != one {
		t.Fatalf("Resolve is not stable: %q != %q", one, again)
	}
	for _, value := range []string{"west", "east", "north"} {
		if one == value {
			return
		}
	}
	t.Fatalf("Resolve returned an unconfigured region %q", one)
}

func TestResolverEmptyConfigurationFallsBack(t *testing.T) {
	if got := NewResolver("v1", nil).Resolve("issuer", "subject"); got != "local" {
		t.Fatalf("Resolve empty configuration = %q, want local", got)
	}
}
