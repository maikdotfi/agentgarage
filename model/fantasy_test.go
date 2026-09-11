package model

import "testing"

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(Config{Provider: "not-real"})
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestNewSupportsConfiguredProviders(t *testing.T) {
	for _, provider := range []Provider{
		"",
		ProviderAnthropic,
		ProviderOpenAI,
		ProviderGoogle,
	} {
		t.Run(string(provider), func(t *testing.T) {
			got, err := New(Config{Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("expected a model")
			}
		})
	}
}

func TestHeadersWithAuthorization(t *testing.T) {
	t.Run("derives bearer token from API key", func(t *testing.T) {
		headers := headersWithAuthorization(nil, "secret")
		if got := headers["Authorization"]; got != "Bearer secret" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer secret")
		}
	})

	t.Run("preserves explicit authorization", func(t *testing.T) {
		headers := headersWithAuthorization(
			map[string]string{"Authorization": "Custom secret"},
			"api-key",
		)
		if got := headers["Authorization"]; got != "Custom secret" {
			t.Fatalf("Authorization = %q, want %q", got, "Custom secret")
		}
	})

	t.Run("does not mutate caller headers", func(t *testing.T) {
		original := map[string]string{"X-Custom": "value"}
		headersWithAuthorization(original, "secret")
		if _, exists := original["Authorization"]; exists {
			t.Fatal("caller headers were mutated")
		}
	})
}

// A model that reasons before it answers spends the same budget on both, so a
// request asking for something long and a thinking configuration that needs
// room are floors rather than alternatives.
func TestOutputBudgetTakesTheLargerFloor(t *testing.T) {
	for _, tc := range []struct {
		name                string
		requested, thinking int64
		want                int64 // 0 means "leave the provider's default"
	}{
		{name: "neither", want: 0},
		{name: "only the request asks", requested: 32000, want: 32000},
		{name: "only thinking needs it", thinking: 16000, want: 16000},
		{name: "the request asks for more", requested: 32000, thinking: 16000, want: 32000},
		{name: "thinking needs more", requested: 2000, thinking: 16000, want: 16000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := outputBudget(tc.requested, tc.thinking)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("budget = %d, want the provider default", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("budget = provider default, want a ceiling")
			}
			if *got != tc.want {
				t.Errorf("budget = %d, want %d", *got, tc.want)
			}
		})
	}
}
