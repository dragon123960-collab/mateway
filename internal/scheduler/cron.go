package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronExpr struct {
	minute cronField
	hour   cronField
	day    cronField
	month  cronField
	week   cronField
}

type cronField struct {
	min      int
	max      int
	wildcard bool
	allowed  map[int]bool
}

func parseCronExpr(expr string) (*cronExpr, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron expr must have 5 fields, got %d", len(parts))
	}
	minute, err := parseCronField(parts[0], 0, 59, false)
	if err != nil {
		return nil, fmt.Errorf("parse minute field: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23, false)
	if err != nil {
		return nil, fmt.Errorf("parse hour field: %w", err)
	}
	day, err := parseCronField(parts[2], 1, 31, false)
	if err != nil {
		return nil, fmt.Errorf("parse day field: %w", err)
	}
	month, err := parseCronField(parts[3], 1, 12, false)
	if err != nil {
		return nil, fmt.Errorf("parse month field: %w", err)
	}
	week, err := parseCronField(parts[4], 0, 7, true)
	if err != nil {
		return nil, fmt.Errorf("parse weekday field: %w", err)
	}
	return &cronExpr{
		minute: minute,
		hour:   hour,
		day:    day,
		month:  month,
		week:   week,
	}, nil
}

func (c *cronExpr) NextAfter(base time.Time, loc *time.Location) time.Time {
	candidate := base.In(loc).Add(time.Minute).Truncate(time.Minute)
	const maxIterations = 60 * 24 * 366 * 5
	for i := 0; i < maxIterations; i++ {
		if c.matches(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return candidate
}

func (c *cronExpr) matches(t time.Time) bool {
	if !c.minute.matches(t.Minute()) || !c.hour.matches(t.Hour()) || !c.month.matches(int(t.Month())) {
		return false
	}
	dayMatch := c.day.matches(t.Day())
	weekDay := int(t.Weekday())
	if weekDay == 0 {
		weekDay = 7
	}
	weekMatch := c.week.matches(weekDay)
	switch {
	case c.day.wildcard && c.week.wildcard:
		return true
	case c.day.wildcard:
		return weekMatch
	case c.week.wildcard:
		return dayMatch
	default:
		return dayMatch || weekMatch
	}
}

func (f cronField) matches(value int) bool {
	if f.wildcard {
		return true
	}
	return f.allowed[value]
}

func parseCronField(raw string, min, max int, sundayAlias bool) (cronField, error) {
	raw = strings.TrimSpace(raw)
	field := cronField{
		min:     min,
		max:     max,
		allowed: map[int]bool{},
	}
	if raw == "*" {
		field.wildcard = true
		return field, nil
	}
	parts := strings.Split(raw, ",")
	for _, item := range parts {
		if err := addCronSegment(field.allowed, strings.TrimSpace(item), min, max, sundayAlias); err != nil {
			return cronField{}, err
		}
	}
	if len(field.allowed) == 0 {
		return cronField{}, fmt.Errorf("empty cron field %q", raw)
	}
	return field, nil
}

func addCronSegment(allowed map[int]bool, raw string, min, max int, sundayAlias bool) error {
	if raw == "" {
		return fmt.Errorf("empty cron segment")
	}
	step := 1
	base := raw
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		base = strings.TrimSpace(parts[0])
		value, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || value <= 0 {
			return fmt.Errorf("invalid step %q", raw)
		}
		step = value
	}
	rangeMin, rangeMax, err := parseCronRange(base, min, max, sundayAlias)
	if err != nil {
		return err
	}
	for value := rangeMin; value <= rangeMax; value += step {
		mapped := value
		if sundayAlias && value == 7 {
			mapped = 0
		}
		allowed[mapped] = true
		if sundayAlias && mapped == 0 {
			allowed[7] = true
		}
	}
	return nil
}

func parseCronRange(raw string, min, max int, sundayAlias bool) (int, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "*" || raw == "" {
		return min, max, nil
	}
	if strings.Contains(raw, "-") {
		parts := strings.SplitN(raw, "-", 2)
		start, err := parseCronValue(parts[0], min, max, sundayAlias)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseCronValue(parts[1], min, max, sundayAlias)
		if err != nil {
			return 0, 0, err
		}
		if end < start {
			return 0, 0, fmt.Errorf("invalid range %q", raw)
		}
		return start, end, nil
	}
	value, err := parseCronValue(raw, min, max, sundayAlias)
	if err != nil {
		return 0, 0, err
	}
	return value, value, nil
}

func parseCronValue(raw string, min, max int, sundayAlias bool) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid cron value %q", raw)
	}
	if sundayAlias && value == 7 {
		return value, nil
	}
	if value < min || value > max {
		return 0, fmt.Errorf("cron value %d out of range [%d,%d]", value, min, max)
	}
	return value, nil
}
