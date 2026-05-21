package schedule

import (
	"strings"
	"time"
)

type DraftCheck struct {
	Ready          bool
	MissingFields  map[string]string
	Questions      []string
	ClarifyMessage string
}

func CheckDraft(input CreateInput) DraftCheck {
	missing := map[string]string{}
	var questions []string
	if strings.TrimSpace(input.Title) == "" {
		missing["title"] = ""
		questions = append(questions, "What should I call this scheduled task?")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		missing["prompt"] = ""
		questions = append(questions, "What should the agent do when this schedule runs?")
	}
	switch draftScheduleKind(input) {
	case "daily":
		if strings.TrimSpace(input.DailyAt) == "" {
			missing["daily_at"] = ""
			questions = append(questions, "What time should it run each day? Use HH:MM.")
		} else if _, _, ok := parseClock(input.DailyAt); !ok {
			missing["daily_at"] = strings.TrimSpace(input.DailyAt)
			questions = append(questions, "The run time must use HH:MM. What daily time should I use?")
		}
	case "weekly":
		if strings.TrimSpace(input.Weekday) == "" && len(input.Weekdays) == 0 {
			missing["weekday"] = ""
			questions = append(questions, "Which weekday or weekdays should it run?")
		}
		if strings.TrimSpace(input.WeeklyAt) == "" {
			missing["weekly_at"] = ""
			questions = append(questions, "What time should it run on that weekday? Use HH:MM.")
		} else if _, _, ok := parseClock(input.WeeklyAt); !ok {
			missing["weekly_at"] = strings.TrimSpace(input.WeeklyAt)
			questions = append(questions, "The weekly run time must use HH:MM. What time should I use?")
		}
	case "monthly":
		if input.MonthlyDay < 1 || input.MonthlyDay > 31 {
			missing["monthly_day"] = ""
			questions = append(questions, "Which day of the month should it run? Use 1-31.")
		}
		if strings.TrimSpace(input.MonthlyAt) == "" {
			missing["monthly_at"] = ""
			questions = append(questions, "What time should it run on that day? Use HH:MM.")
		} else if _, _, ok := parseClock(input.MonthlyAt); !ok {
			missing["monthly_at"] = strings.TrimSpace(input.MonthlyAt)
			questions = append(questions, "The monthly run time must use HH:MM. What time should I use?")
		}
	case "interval":
		if strings.TrimSpace(input.Interval) == "" {
			missing["interval"] = ""
			questions = append(questions, "How often should it run? Use a duration such as 2h.")
		} else if _, err := time.ParseDuration(input.Interval); err != nil {
			missing["interval"] = strings.TrimSpace(input.Interval)
			questions = append(questions, "The interval must be a duration such as 2h. How often should it run?")
		}
	default:
		missing["daily_at"] = ""
		questions = append(questions, "When should it run?")
	}
	if len(missing) == 0 {
		return DraftCheck{Ready: true}
	}
	return DraftCheck{
		Ready:          false,
		MissingFields:  missing,
		Questions:      questions,
		ClarifyMessage: strings.Join(questions, "\n"),
	}
}

func draftScheduleKind(input CreateInput) string {
	if strings.TrimSpace(input.Interval) != "" {
		return "interval"
	}
	if strings.TrimSpace(input.MonthlyAt) != "" || input.MonthlyDay > 0 {
		return "monthly"
	}
	if strings.TrimSpace(input.WeeklyAt) != "" || strings.TrimSpace(input.Weekday) != "" || len(input.Weekdays) > 0 {
		return "weekly"
	}
	return "daily"
}

func ApplyDraftFields(input CreateInput, fields map[string]string) CreateInput {
	for key, value := range fields {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "id":
			input.ID = value
		case "title":
			input.Title = value
		case "prompt":
			input.Prompt = value
		case "daily_at":
			input.DailyAt = value
		case "weekly_at":
			input.WeeklyAt = value
		case "weekday":
			input.Weekday = value
		case "weekdays":
			input.Weekdays = splitList(value)
		case "monthly_at":
			input.MonthlyAt = value
		case "monthly_day":
			input.MonthlyDay = parsePositiveInt(value)
		case "interval":
			input.Interval = value
		case "agent_id":
			input.AgentID = value
		case "channel":
			input.Channel = value
		case "thread_id":
			input.ThreadID = value
		case "user_id":
			input.UserID = value
		case "delivery_mode":
			input.DeliveryMode = value
		case "delivery_path":
			input.DeliveryPath = value
		}
	}
	return input
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parsePositiveInt(value string) int {
	n := 0
	for _, ch := range strings.TrimSpace(value) {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
