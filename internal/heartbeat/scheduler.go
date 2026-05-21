package heartbeat

import (
	"context"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
)

const defaultSchedulerInterval = 5 * time.Minute

var allowedScheduledJobs = map[string]struct{}{
	JobMemoryLint:          {},
	JobMemoryDailyReview:   {},
	JobMemoryRecentCompact: {},
	JobMemoryIndexRebuild:  {},
}

type ScheduledJob struct {
	AgentID string
	Job     string
}

type Scheduler struct {
	Runner   Runner
	Interval time.Duration
}

func NewScheduler(cfg *config.Root) Scheduler {
	return Scheduler{Runner: NewRunner(cfg), Interval: defaultSchedulerInterval}
}

func (s Scheduler) Start(ctx context.Context) {
	if s.Runner.Config == nil || !s.Runner.Config.Scheduler.Enabled {
		return
	}
	interval := s.Interval
	if interval <= 0 {
		interval = defaultSchedulerInterval
	}
	go func() {
		_ = s.RunDue(time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = s.RunDue(now)
			}
		}
	}()
}

func (s Scheduler) RunDue(now time.Time) error {
	state, _, err := s.Runner.Status()
	if err != nil {
		return err
	}
	for _, job := range DueJobs(s.Runner.Config, state, now) {
		if _, err := s.Runner.Run(RunOptions{AgentID: job.AgentID, Job: job.Job, Now: now}); err != nil {
			return err
		}
	}
	return nil
}

func DueJobs(cfg *config.Root, state State, now time.Time) []ScheduledJob {
	if cfg == nil || !cfg.Scheduler.Enabled {
		return nil
	}
	loc := schedulerLocation(cfg)
	now = now.In(loc)
	if now.IsZero() {
		return nil
	}
	var due []ScheduledJob
	for _, profile := range cfg.Agents.Profiles {
		if !profile.Heartbeat.Enabled {
			continue
		}
		agentID := strings.TrimSpace(profile.ID)
		if agentID == "" {
			continue
		}
		if inQuietHours(now, profile.Heartbeat.QuietHours) {
			continue
		}
		if !pastDailyTime(now, profile.Heartbeat.Schedule.DailyAt) {
			continue
		}
		for _, job := range profile.Heartbeat.Jobs {
			job = strings.TrimSpace(job)
			if _, ok := allowedScheduledJobs[job]; !ok {
				continue
			}
			if ranToday(state, agentID, job, now) {
				continue
			}
			due = append(due, ScheduledJob{AgentID: agentID, Job: job})
		}
	}
	return due
}

func schedulerLocation(cfg *config.Root) *time.Location {
	name := strings.TrimSpace(cfg.Scheduler.Timezone)
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func pastDailyTime(now time.Time, dailyAt string) bool {
	hour, minute, ok := parseClock(dailyAt)
	if !ok {
		return true
	}
	dueAt := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	return !now.Before(dueAt)
}

func ranToday(state State, agentID, job string, now time.Time) bool {
	for _, item := range state.Jobs {
		if item.AgentID != agentID || item.Job != job || item.LastRunAt.IsZero() {
			continue
		}
		last := item.LastRunAt.In(now.Location())
		if last.Year() == now.Year() && last.YearDay() == now.YearDay() {
			return true
		}
	}
	return false
}

func inQuietHours(now time.Time, quiet config.HeartbeatQuietHours) bool {
	startHour, startMinute, okStart := parseClock(quiet.Start)
	endHour, endMinute, okEnd := parseClock(quiet.End)
	if !okStart || !okEnd {
		return false
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), startHour, startMinute, 0, 0, now.Location())
	end := time.Date(now.Year(), now.Month(), now.Day(), endHour, endMinute, 0, 0, now.Location())
	if start.Equal(end) {
		return false
	}
	if end.After(start) {
		return !now.Before(start) && now.Before(end)
	}
	return !now.Before(start) || now.Before(end)
}

func parseClock(value string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, okHour := parseTwoDigitClockPart(parts[0], 23)
	minute, okMinute := parseTwoDigitClockPart(parts[1], 59)
	return hour, minute, okHour && okMinute
}

func parseTwoDigitClockPart(value string, max int) (int, bool) {
	if value == "" || len(value) > 2 {
		return 0, false
	}
	n := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	if n < 0 || n > max {
		return 0, false
	}
	return n, true
}
