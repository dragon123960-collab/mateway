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
		questions = append(questions, "这个定时任务叫什么名字？")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		missing["prompt"] = ""
		questions = append(questions, "定时触发时，希望我具体做什么？")
	}
	switch draftScheduleKind(input) {
	case "once":
		if strings.TrimSpace(input.RunAt) == "" {
			missing["run_at"] = ""
			questions = append(questions, "一次性任务什么时候运行？请用 RFC3339 时间，例如 2026-05-25T10:30:00+08:00。")
		} else if _, err := parseRunAt(input.RunAt); err != nil {
			missing["run_at"] = strings.TrimSpace(input.RunAt)
			questions = append(questions, "一次性任务时间需要用 RFC3339 格式，例如 2026-05-25T10:30:00+08:00。")
		}
	case "daily":
		if strings.TrimSpace(input.DailyAt) == "" {
			missing["daily_at"] = ""
			questions = append(questions, "每天几点运行？请用 HH:MM 格式。")
		} else if _, _, ok := parseClock(input.DailyAt); !ok {
			missing["daily_at"] = strings.TrimSpace(input.DailyAt)
			questions = append(questions, "运行时间需要用 HH:MM 格式。每天几点运行？")
		}
	case "weekly":
		if strings.TrimSpace(input.Weekday) == "" && len(input.Weekdays) == 0 {
			missing["weekday"] = ""
			questions = append(questions, "每周哪一天或哪几天运行？")
		}
		if strings.TrimSpace(input.WeeklyAt) == "" {
			missing["weekly_at"] = ""
			questions = append(questions, "这些日期的几点运行？请用 HH:MM 格式。")
		} else if _, _, ok := parseClock(input.WeeklyAt); !ok {
			missing["weekly_at"] = strings.TrimSpace(input.WeeklyAt)
			questions = append(questions, "每周运行时间需要用 HH:MM 格式。几点运行？")
		}
	case "monthly":
		if input.MonthlyDay < 1 || input.MonthlyDay > 31 {
			missing["monthly_day"] = ""
			questions = append(questions, "每月几号运行？请填写 1-31。")
		}
		if strings.TrimSpace(input.MonthlyAt) == "" {
			missing["monthly_at"] = ""
			questions = append(questions, "当天几点运行？请用 HH:MM 格式。")
		} else if _, _, ok := parseClock(input.MonthlyAt); !ok {
			missing["monthly_at"] = strings.TrimSpace(input.MonthlyAt)
			questions = append(questions, "每月运行时间需要用 HH:MM 格式。几点运行？")
		}
	case "interval":
		if strings.TrimSpace(input.Interval) == "" {
			missing["interval"] = ""
			questions = append(questions, "每隔多久重复运行一次？请用类似 2h 的时长格式。")
		} else if _, err := time.ParseDuration(input.Interval); err != nil {
			missing["interval"] = strings.TrimSpace(input.Interval)
			questions = append(questions, "重复间隔需要是类似 2h 的时长格式。每隔多久运行一次？")
		}
	default:
		missing["schedule"] = ""
		questions = append(questions, "这个任务是一次性运行，还是每天/每周/每月/每隔一段时间重复运行？")
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
	if strings.TrimSpace(input.RunAt) != "" {
		return "once"
	}
	if strings.TrimSpace(input.Interval) != "" {
		return "interval"
	}
	if strings.TrimSpace(input.MonthlyAt) != "" || input.MonthlyDay > 0 {
		return "monthly"
	}
	if strings.TrimSpace(input.WeeklyAt) != "" || strings.TrimSpace(input.Weekday) != "" || len(input.Weekdays) > 0 {
		return "weekly"
	}
	if strings.TrimSpace(input.DailyAt) != "" {
		return "daily"
	}
	return ""
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
		case "run_at":
			input.RunAt = value
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
