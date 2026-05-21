package schedule

import "testing"

func TestCheckDraftReturnsMissingFields(t *testing.T) {
	check := CheckDraft(CreateInput{Title: "Daily AI Trends"})
	if check.Ready {
		t.Fatal("expected draft to require more fields")
	}
	if _, ok := check.MissingFields["prompt"]; !ok {
		t.Fatalf("expected prompt missing, got %#v", check.MissingFields)
	}
	if _, ok := check.MissingFields["daily_at"]; !ok {
		t.Fatalf("expected daily_at missing, got %#v", check.MissingFields)
	}
	if len(check.Questions) != 2 || check.ClarifyMessage == "" {
		t.Fatalf("expected questions, got %#v", check)
	}
}

func TestCheckDraftRejectsInvalidDailyAt(t *testing.T) {
	check := CheckDraft(CreateInput{Title: "Daily AI Trends", Prompt: "Collect AI trend articles.", DailyAt: "morning"})
	if check.Ready {
		t.Fatal("expected invalid time to require clarification")
	}
	if got := check.MissingFields["daily_at"]; got != "morning" {
		t.Fatalf("expected daily_at value kept, got %q", got)
	}
}

func TestCheckDraftReady(t *testing.T) {
	check := CheckDraft(CreateInput{Title: "Daily AI Trends", Prompt: "Collect AI trend articles.", DailyAt: "09:00"})
	if !check.Ready || len(check.MissingFields) != 0 {
		t.Fatalf("expected ready draft, got %#v", check)
	}
}

func TestCheckDraftReadyForWeeklyAndInterval(t *testing.T) {
	weekly := CheckDraft(CreateInput{Title: "Weekly Report", Prompt: "Summarize issues.", WeeklyAt: "09:00", Weekday: "friday"})
	if !weekly.Ready {
		t.Fatalf("expected weekly ready, got %#v", weekly)
	}
	interval := CheckDraft(CreateInput{Title: "Status Check", Prompt: "Check status.", Interval: "2h"})
	if !interval.Ready {
		t.Fatalf("expected interval ready, got %#v", interval)
	}
}

func TestApplyDraftFields(t *testing.T) {
	input := ApplyDraftFields(CreateInput{Title: "Daily AI Trends"}, map[string]string{
		"prompt":   "Collect AI trend articles.",
		"daily_at": "09:00",
	})
	if input.Prompt == "" || input.DailyAt != "09:00" {
		t.Fatalf("unexpected input %#v", input)
	}
}
