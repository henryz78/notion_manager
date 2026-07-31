package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParseNDJSONStreamAcceptsLargeUpstreamEvent(t *testing.T) {
	largeEvent := `{"type":"ignored","padding":"` + strings.Repeat("x", 2*1024*1024) + `"}`
	answerEvent := `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"ok"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`
	stream := largeEvent + "\n" + answerEvent

	var got strings.Builder
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		got.WriteString(delta)
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error for event above the old 1 MiB limit: %v", err)
	}
	if got.String() != "ok" {
		t.Fatalf("unexpected parser output: %q", got.String())
	}
}

func TestCleanAllLangTagsHoldsSplitOpeningMarker(t *testing.T) {
	for _, raw := range []string{"<", "<l", "<la", "<lan", "<lang", `<lang primary="en"`} {
		if got := cleanAllLangTags(raw); got != "" {
			t.Fatalf("cleanAllLangTags(%q) = %q, want empty until the internal tag is complete", raw, got)
		}
	}
	if got := cleanAllLangTags(`<lang primary="en"/>answer`); got != "answer" {
		t.Fatalf("complete language tag was not removed: %q", got)
	}
	if got := cleanAllLangTags("prefix <label>"); got != "prefix <label>" {
		t.Fatalf("ordinary angle-bracket text changed: %q", got)
	}
}

func TestParseNDJSONStreamPreservesIncompleteLangLikeSuffixAtFinish(t *testing.T) {
	for _, suffix := range []string{"<", "<l", "<la", "<lan", "<lang"} {
		want := "literal " + suffix
		event, err := json.Marshal(map[string]interface{}{
			"type": "agent-inference",
			"id":   "step1",
			"value": []map[string]interface{}{{
				"type":    "text",
				"content": want,
			}},
			"finishedAt":   1,
			"inputTokens":  1,
			"outputTokens": 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		err = parseNDJSONStream(bytes.NewReader(event), "", func(delta string, _ bool, _ *UsageInfo) {
			got.WriteString(delta)
		}, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("suffix %q: %v", suffix, err)
		}
		if got.String() != want {
			t.Fatalf("suffix %q: got %q, want %q", suffix, got.String(), want)
		}
	}
}

func TestParseNDJSONStreamPreservesLiteralCitationLikeSuffixAtFinish(t *testing.T) {
	for _, suffix := range []string{"[", "[^", "[^abc"} {
		want := "literal " + suffix
		event, err := json.Marshal(map[string]interface{}{
			"type": "agent-inference",
			"id":   "step1",
			"value": []map[string]interface{}{{
				"type":    "text",
				"content": want,
			}},
			"finishedAt":   1,
			"inputTokens":  1,
			"outputTokens": 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		err = parseNDJSONStream(bytes.NewReader(event), "", func(delta string, _ bool, _ *UsageInfo) {
			got.WriteString(delta)
		}, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("suffix %q: %v", suffix, err)
		}
		if got.String() != want {
			t.Fatalf("suffix %q: got %q, want %q", suffix, got.String(), want)
		}
	}
}

func TestParseNDJSONStreamRejectsMalformedAndTruncatedStreams(t *testing.T) {
	t.Run("malformed event", func(t *testing.T) {
		err := parseNDJSONStream(
			bytes.NewBufferString("{not-json}\n"),
			"",
			func(string, bool, *UsageInfo) {},
			nil, nil, nil, nil, nil, nil,
		)
		if err == nil || !strings.Contains(err.Error(), "malformed notion NDJSON") {
			t.Fatalf("error=%v, want malformed NDJSON error", err)
		}
	})

	t.Run("partial text without terminal event", func(t *testing.T) {
		var got strings.Builder
		err := parseNDJSONStream(
			bytes.NewBufferString(`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"partial"}]}`),
			"",
			func(delta string, _ bool, _ *UsageInfo) { got.WriteString(delta) },
			nil, nil, nil, nil, nil, nil,
		)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error=%v, want io.ErrUnexpectedEOF", err)
		}
		if got.String() != "partial" {
			t.Fatalf("partial output=%q, want partial", got.String())
		}
	})

	t.Run("empty stream without terminal event", func(t *testing.T) {
		err := parseNDJSONStream(
			bytes.NewBufferString(`{"type":"ignored"}`),
			"",
			func(string, bool, *UsageInfo) {},
			nil, nil, nil, nil, nil, nil,
		)
		if !errors.Is(err, ErrEmptyResponse) {
			t.Fatalf("error=%v, want ErrEmptyResponse", err)
		}
	})

	t.Run("finished internal search without final answer", func(t *testing.T) {
		err := parseNDJSONStream(
			bytes.NewBufferString(`{"type":"agent-inference","id":"search-step","value":[{"type":"tool_use","id":"toolu_1","name":"search","content":"{}"}],"finishedAt":1,"inputTokens":10,"outputTokens":1}`),
			"",
			func(string, bool, *UsageInfo) {},
			nil, nil, nil, nil, nil, nil,
		)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error=%v, want io.ErrUnexpectedEOF", err)
		}
	})
}

func TestParseResearcherStreamRejectsMalformedEvent(t *testing.T) {
	err := parseResearcherStream(
		bytes.NewBufferString("{not-json}\n"),
		"",
		func(string, bool, *UsageInfo) {},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "malformed notion researcher NDJSON") {
		t.Fatalf("error=%v, want malformed researcher NDJSON error", err)
	}
}

func TestParseResearcherStreamRejectsWrongShapeAfterPartialReport(t *testing.T) {
	var report strings.Builder
	stream := strings.Join([]string{
		`{"type":"researcher-report","id":"report","value":"partial report"}`,
		`{"type":"researcher-report","id":"report","value":{}}`,
	}, "\n")
	err := parseResearcherStream(
		bytes.NewBufferString(stream),
		"",
		func(delta string, _ bool, _ *UsageInfo) { report.WriteString(delta) },
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "malformed researcher-report") {
		t.Fatalf("error=%v, want malformed researcher-report error", err)
	}
	if report.String() != "partial report" {
		t.Fatalf("partial report=%q", report.String())
	}
}

func TestParseResearcherStreamRejectsMissingReport(t *testing.T) {
	err := parseResearcherStream(
		bytes.NewBufferString(`{"type":"researcher-text-observation","value":"query"}`),
		"",
		func(string, bool, *UsageInfo) {},
		nil,
		nil,
	)
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("error=%v, want ErrEmptyResponse", err)
	}
}

func TestParseNDJSONPatchInternalSearchUsageIsNotTerminal(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"patch","v":[{"o":"a","p":"/s/0/value/-","v":{"type":"tool_use","id":"toolu_1","name":"search","content":"{\"web\":{\"queries\":[\"weather\"]}}"}}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/0/inputTokens","v":10},{"o":"a","p":"/s/0/outputTokens","v":2}]}`,
	}, "\n")
	err := parseNDJSONStream(
		bytes.NewBufferString(stream),
		"",
		func(string, bool, *UsageInfo) {},
		nil, nil, nil, nil, nil, nil,
	)
	if err == nil {
		t.Fatal("patch usage falsely completed an unfinished internal search")
	}
}

func TestParseNDJSONStreamClassifiesPromptTooLong(t *testing.T) {
	err := parseNDJSONStream(bytes.NewBufferString(`{"type":"error","message":"Prompt too long."}`), "", func(string, bool, *UsageInfo) {}, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("error=%v want ErrPromptTooLong", err)
	}
}

func TestParseResearcherStreamClassifiesPromptTooLong(t *testing.T) {
	err := parseResearcherStream(bytes.NewBufferString(`{"type":"error","message":"Prompt too long."}`), "", func(string, bool, *UsageInfo) {}, nil, nil)
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("error=%v want ErrPromptTooLong", err)
	}
}

func TestParseNDJSONStreamCountsEachFinishedStepUsageOnce(t *testing.T) {
	step1 := `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"first"}],"finishedAt":1,"inputTokens":16000,"outputTokens":20}`
	step2 := `{"type":"agent-inference","id":"step2","value":[{"type":"text","content":"second"}],"finishedAt":2,"inputTokens":500,"outputTokens":5}`
	stream := strings.Join([]string{step1, step1, step2, step2}, "\n")

	var finalUsage UsageInfo
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if usage != nil {
			finalUsage = *usage
		}
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if finalUsage.PromptTokens != 16_000 || finalUsage.CompletionTokens != 25 || finalUsage.TotalTokens != 16_025 {
		t.Fatalf("usage did not preserve peak input and distinct output totals: %+v", finalUsage)
	}
}

func TestParseNDJSONStreamCountsEachPatchUsagePathOnce(t *testing.T) {
	patch1 := `{"type":"patch","v":[{"o":"a","p":"/s/0/inputTokens","v":16000},{"o":"a","p":"/s/0/outputTokens","v":20}]}`
	patch2 := `{"type":"patch","v":[{"o":"a","p":"/s/1/inputTokens","v":500},{"o":"a","p":"/s/1/outputTokens","v":5}]}`
	stream := strings.Join([]string{patch1, patch1, patch2, patch2}, "\n")

	var finalUsage UsageInfo
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if usage != nil {
			finalUsage = *usage
		}
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if finalUsage.PromptTokens != 16_000 || finalUsage.CompletionTokens != 25 || finalUsage.TotalTokens != 16_025 {
		t.Fatalf("patch usage did not preserve peak input and distinct output totals: %+v", finalUsage)
	}
}

func TestParseNDJSONStreamEmitsWorkflowProcessThinking(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"agent-inference","id":"step1","value":[{"type":"thinking","content":"Let me search"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"thinking","content":"Let me search for information about Notion Agent."},{"type":"tool_use","id":"toolu_1","name":"search","content":"{\"web\":{\"queries\":[\"What is Notion Agent"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"thinking","content":"Let me search for information about Notion Agent."},{"type":"tool_use","id":"toolu_1","name":"search","content":"{\"web\":{\"queries\":[\"What is Notion Agent and what can you do with it?\"]}}"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`,
		`{"type":"agent-search-extracted-results","toolCallId":"toolu_1","results":[{"id":"webpage://?url=https%3A%2F%2Fexample.com%2Fagent","title":"How to work with your Agent"},{"id":"webpage://?url=https%3A%2F%2Fexample.com%2Fnotion-agent","title":"Notion Agent"}]}`,
		`{"type":"agent-inference","id":"step2","value":[{"type":"thinking","content":"Let me summarize"}]}`,
		`{"type":"agent-inference","id":"step2","value":[{"type":"thinking","content":"Let me summarize the search results."},{"type":"text","content":"Final answer text."}],"finishedAt":2,"inputTokens":20,"outputTokens":4}`,
	}, "\n")

	var thinking strings.Builder
	var text strings.Builder
	var events []string
	doneCount := 0

	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			events = append(events, "text")
			text.WriteString(delta)
		}
	}, nil, nil, func(delta string, done bool, signature string) {
		if done {
			doneCount++
			events = append(events, "thinking_done")
			return
		}
		if delta != "" {
			events = append(events, "thinking")
			thinking.WriteString(delta)
		}
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error: %v", err)
	}

	thinkingText := thinking.String()
	if !strings.Contains(thinkingText, "Let me search for information about Notion Agent.") {
		t.Fatalf("expected initial workflow thinking, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "**Web Search**: What is Notion Agent and what can you do with it?") {
		t.Fatalf("expected search query summary, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "**Searching**: What is Notion Agent and what can you do with it?") {
		t.Fatalf("expected search start status, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "**Search Complete**: What is Notion Agent and what can you do with it?") {
		t.Fatalf("expected search completion status, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "**Found 2 Results**") {
		t.Fatalf("expected result count summary, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "How to work with your Agent") || !strings.Contains(thinkingText, "Notion Agent") {
		t.Fatalf("expected result titles in thinking output, got %q", thinkingText)
	}
	if !strings.Contains(thinkingText, "Let me summarize the search results.") {
		t.Fatalf("expected summarization thinking, got %q", thinkingText)
	}
	if text.String() != "Final answer text." {
		t.Fatalf("unexpected text output: %q", text.String())
	}
	if doneCount != 1 {
		t.Fatalf("expected exactly one thinking_done callback, got %d", doneCount)
	}

	doneIndex := -1
	textIndex := -1
	for i, event := range events {
		if event == "thinking_done" && doneIndex == -1 {
			doneIndex = i
		}
		if event == "text" && textIndex == -1 {
			textIndex = i
		}
	}
	if doneIndex == -1 || textIndex == -1 || doneIndex > textIndex {
		t.Fatalf("expected thinking_done before first text event, got events=%v", events)
	}
}

func TestParseNDJSONStreamTrimsIncompleteCitationRewrites(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4.6**：2026 年 2 月 "}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4.6**：2026 年 2 月 17 日发布[^{{https://www.anthropic.com/news/claude-sonnet-4-6"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4.6**：2026 年 2 月 17 日发布[^view://artifact-123]\n- **速度快**"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`,
	}, "\n")

	var got strings.Builder
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			got.WriteString(delta)
		}
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error: %v", err)
	}

	want := "- **Claude Sonnet 4.6**：2026 年 2 月 17 日发布[^https://www.anthropic.com/news/claude-sonnet-4-6]\n- **速度快**"
	if got.String() != want {
		t.Fatalf("unexpected parser output: got %q want %q", got.String(), want)
	}
}

func TestParseNDJSONStreamUsesPendingFragmentForFirstNewInternalCitation(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude 3 Sonnet**：2024年3月4日发布[^{{https://en.wikipedia.org/wiki/Claude_(language_model)}}]\n- **Claude 3.5 Sonnet**：2024年6月20日发布[^{{https://en.wikipedia.org/wiki/Claude_(language_model)}}]\n- **Claude Sonnet 4**：2025年5月22日发布[^{{https://www.anthropic.com/news/claude-4"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude 3 Sonnet**：2024年3月4日发布[^{{https://en.wikipedia.org/wiki/Claude_(language_model)}}]\n- **Claude 3.5 Sonnet**：2024年6月20日发布[^{{https://en.wikipedia.org/wiki/Claude_(language_model)}}]\n- **Claude Sonnet 4**：2025年5月22日发布[^view://artifact-123]\n- **Claude Sonnet 4.6**：2026年2月17日发布[^view://artifact-123]"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`,
	}, "\n")

	var got strings.Builder
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			got.WriteString(delta)
		}
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error: %v", err)
	}

	want := "- **Claude 3 Sonnet**：2024年3月4日发布[^https://en.wikipedia.org/wiki/Claude_(language_model)]\n" +
		"- **Claude 3.5 Sonnet**：2024年6月20日发布[^https://en.wikipedia.org/wiki/Claude_(language_model)]\n" +
		"- **Claude Sonnet 4**：2025年5月22日发布[^https://www.anthropic.com/news/claude-4]\n" +
		"- **Claude Sonnet 4.6**：2026年2月17日发布[^view://artifact-123]"
	if got.String() != want {
		t.Fatalf("unexpected parser output: got %q want %q", got.String(), want)
	}
}

func TestParseNDJSONStreamDoesNotRewriteToolCitationsFromObservedFragments(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4.5**：2025年9月29日发布[^{{https://www.anthropic.com/news/claude-son"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4.5**：2025年9月29日发布[^toolu_test123]\n- **Claude Sonnet 4.6**：2026年2月17日发布[^toolu_test123]"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`,
	}, "\n")

	var got strings.Builder
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			got.WriteString(delta)
		}
	}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error: %v", err)
	}

	want := "- **Claude Sonnet 4.5**：2025年9月29日发布[^toolu_test123]\n" +
		"- **Claude Sonnet 4.6**：2026年2月17日发布[^toolu_test123]"
	if got.String() != want {
		t.Fatalf("unexpected parser output: got %q want %q", got.String(), want)
	}
}

func TestParseNDJSONStreamKeepsInternalCitationWhenObservedHTTPPrefixIsAmbiguous(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4**：2025年5月22日发布[^{{https://www.anthropic.com/news/claude"}]}`,
		`{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"- **Claude Sonnet 4**：2025年5月22日发布[^view://artifact-123]"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`,
	}, "\n")

	known := []string{
		"https://www.anthropic.com/news/claude-3-family",
		"https://www.anthropic.com/news/claude-4",
		"https://www.anthropic.com/news/claude-sonnet-4-5",
		"https://www.anthropic.com/news/claude-sonnet-4-6",
	}

	var got strings.Builder
	err := parseNDJSONStream(bytes.NewBufferString(stream), "", func(delta string, done bool, usage *UsageInfo) {
		if delta != "" {
			got.WriteString(delta)
		}
	}, nil, nil, nil, &known, nil, nil)
	if err != nil {
		t.Fatalf("parseNDJSONStream returned error: %v", err)
	}

	want := "- **Claude Sonnet 4**：2025年5月22日发布[^view://artifact-123]"
	if got.String() != want {
		t.Fatalf("unexpected parser output: got %q want %q", got.String(), want)
	}
}
