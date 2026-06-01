package model

import "testing"

func TestParseAnthropicResultUsage(t *testing.T) {
	result, err := parseAnthropicResult([]byte(`{
		"content":[{"type":"text","text":"hello"}],
		"usage":{"input_tokens":12,"output_tokens":5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 17 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestParseOpenAIResponsesResultUsage(t *testing.T) {
	result := parseOpenAIResponsesResult([]byte(`{
		"output_text":"hello",
		"usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28}
	}`))
	if result.Text != "hello" || result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 8 || result.Usage.TotalTokens != 28 {
		t.Fatalf("unexpected result %#v", result)
	}
}
