package models

import (
	"testing"
	"time"

	"opencode2api/internal/config"
)

func TestIsFreeModelNameFallback(t *testing.T) {
	catalog := NewCatalog(config.TierZen, nil)
	if !catalog.IsFreeModel("muse-spark-1.3-contributor-free") {
		t.Fatal("expected -free name to be free")
	}
	if catalog.IsFreeModel("gpt-5-paid") {
		t.Fatal("expected paid model to not be free")
	}
}

func TestIsFreeModelMetadata(t *testing.T) {
	catalog := NewCatalog(config.TierZen, nil)
	zero := 0.0
	one := 1.0
	two := 2.0
	catalog.SetPricingStore(&PricingStore{
		models: map[string]Price{
			"nameless-free-model": {ID: "nameless-free-model", Input: &zero, Output: &zero},
			"paid-model":          {ID: "paid-model", Input: &one, Output: &two},
		},
		updatedAt: time.Now().UTC(),
	})
	if !catalog.IsFreeModel("nameless-free-model") {
		t.Fatal("expected metadata-free model to be free")
	}
	if catalog.IsFreeModel("paid-model") {
		t.Fatal("expected paid model to not be free")
	}
}
