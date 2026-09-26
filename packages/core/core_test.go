package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestCostAdd(t *testing.T) {
	var c costTracker
	c.Add(provider.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.01})
	c.Add(provider.Usage{InputTokens: 200, OutputTokens: 25, CostUSD: 0.02})
	if c.Total.InputTokens != 300 || c.Total.OutputTokens != 75 {
		t.Fatalf("got %+v", c.Total)
	}
	if c.Total.CostUSD < 0.0299 || c.Total.CostUSD > 0.0301 {
		t.Fatalf("got cost %v", c.Total.CostUSD)
	}
}
