package markers

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseOutputBlockAndSurroundingProse(t *testing.T) {
	for _, text := range []string{
		"AIC_OUTPUT\n{\"summary_md\":\"done\",\"changed_files\":[\"a.go\"]}\nAIC_OUTPUT_END",
		"The result follows:\n  AIC_OUTPUT  \n```json\n{\"ok\":true}\n```\n AIC_OUTPUT_END \nThanks.",
	} {
		got, found, err := ParseOutput(text)
		if err != nil || !found {
			t.Fatalf("ParseOutput(%q) = %#v, %v, %v", text, got, found, err)
		}
		if len(got.Values) == 0 {
			t.Fatalf("empty values: %#v", got)
		}
	}
}

func TestParseOutputMalformedAndMissingFence(t *testing.T) {
	for _, text := range []string{
		"AIC_OUTPUT\n{bad}\nAIC_OUTPUT_END",
		"AIC_OUTPUT\n{\"x\":1}",
		"AIC_OUTPUT\n[1,2]\nAIC_OUTPUT_END",
	} {
		if _, _, err := ParseOutput(text); err == nil {
			t.Errorf("ParseOutput(%q) unexpectedly succeeded", text)
		}
	}
}

func TestParseMemoriesSeveralBlocksAndUntrustedScope(t *testing.T) {
	text := "AIC_MEMORIZE\n{\"key\":\"style\",\"content\":\"Use tabs\",\"scope\":\"agent\"}\nAIC_MEMORIZE_END\n" +
		"AIC_MEMORIZE\n{\"key\":\"bad\",\"content\":\"x\",\"scope\":\"run\"}\nAIC_MEMORIZE_END"
	got, err := ParseMemories(text)
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseMemories = %#v, %v", got, err)
	}
	want := []Memories{{Key: "style", Content: "Use tabs", Scope: "agent"}, {Key: "bad", Content: "x", Scope: "run"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memories %#v, want %#v", got, want)
	}
}

func TestFindPublishProposal(t *testing.T) {
	proposal, found := FindPublish("Some text\nAIC_PUBLISH\n  Update issue with this summary.  \nAIC_PUBLISH_END\n")
	if !found || proposal != "Update issue with this summary." {
		t.Fatalf("proposal=%q found=%v", proposal, found)
	}
	if _, found = FindPublish("ordinary output"); found {
		t.Fatal("found absent proposal")
	}
	if _, found = FindPublish("AIC_PUBLISH\nmissing end"); found {
		t.Fatal("accepted unterminated proposal")
	}
	if strings.Contains(proposal, "AIC_PUBLISH") {
		t.Fatal("marker leaked into proposal")
	}
}
