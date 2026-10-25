package main

import (
	"testing"

	"github.com/orlandoburli/ai-company/internal/ranking"
)

func TestRankingCLIOptionsFromEnv(t *testing.T) {
	t.Setenv("AIC_RANKING_CLI", "/opt/tools/opencode")
	t.Setenv("AIC_RANKING_MODEL", "opencode-go/model")
	opts := rankingCLIOptionsFromEnv()
	if opts.Bin != "/opt/tools/opencode" || opts.Model != "opencode-go/model" {
		t.Fatalf("ranking CLI options = %+v", opts)
	}
}

func TestRankingCLIOptionsDefaultBinaryAndOptionalModel(t *testing.T) {
	t.Setenv("AIC_RANKING_CLI", "")
	t.Setenv("AIC_RANKING_MODEL", "")
	opts := rankingCLIOptionsFromEnv()
	if opts.Bin != ranking.DefaultCLIBinary || opts.Model != "" {
		t.Fatalf("ranking CLI options = %+v", opts)
	}
}
