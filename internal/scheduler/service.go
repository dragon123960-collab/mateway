package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RunResult struct {
	RunID  string
	Status string
	Error  string
}

type JobRunner interface {
	RunScheduledJob(context.Context, Job) (RunResult, error)
}

type Service struct {
	Store  Store
	Runner JobRunner
}

func (s Service) Start(ctx context.Context) error {
	if s.Runner == nil {
		return nil
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.runDue(ctx, now)
			}
		}
	}()
	return nil
}

func (s Service) RunNow(ctx context.Context, name string) (Job, error) {
	job, ok, err := s.Store.Get(name)
	if err != nil {
		return Job{}, err
	}
	if !ok {
		return Job{}, fmt.Errorf("schedule %q not found", name)
	}
	return s.runJob(ctx, job, time.Now(), true)
}

func (s Service) runDue(ctx context.Context, now time.Time) {
	jobs, err := s.Store.Due(now)
	if err != nil {
		return
	}
	for _, job := range jobs {
		_, _ = s.runJob(ctx, job, now, false)
	}
}

func (s Service) runJob(ctx context.Context, job Job, now time.Time, force bool) (Job, error) {
	if s.Runner == nil {
		return job, nil
	}
	if !job.Enabled && !force {
		return job, nil
	}

	startedAt := time.Now()
	result, err := s.Runner.RunScheduledJob(ctx, job)
	finishedAt := time.Now()

	job.State.LastRunAt = startedAt
	job.State.LastDurationMs = finishedAt.Sub(startedAt).Milliseconds()
	job.State.LastRunID = stringsOr(result.RunID, "")

	status := stringsOr(result.Status, "ok")
	if err != nil {
		status = "error"
		job.State.LastError = err.Error()
		job.State.ConsecutiveErrors++
	} else if stringsEqualFold(status, "completed") || stringsEqualFold(status, "ok") {
		job.State.LastError = ""
		job.State.ConsecutiveErrors = 0
	} else {
		job.State.LastError = stringsOr(result.Error, result.Status)
		job.State.ConsecutiveErrors++
	}
	job.State.LastRunStatus = status

	nextBase := now
	if force {
		nextBase = finishedAt
	}
	nextRunAt, nextErr := job.Schedule.NextRunAfter(nextBase)
	if nextErr == nil {
		job.State.NextRunAt = nextRunAt
	}
	job.UpdatedAt = finishedAt

	saveErr := s.Store.Save(job)
	appendErr := s.Store.AppendRun(job, status, result.RunID, finishedAt.Sub(startedAt), err)
	if err != nil {
		if saveErr != nil {
			return job, saveErr
		}
		if appendErr != nil {
			return job, appendErr
		}
		return job, err
	}
	if saveErr != nil {
		return job, saveErr
	}
	if appendErr != nil {
		return job, appendErr
	}
	return job, nil
}

func stringsOr(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringsEqualFold(left, right string) bool {
	return strings.EqualFold(left, right)
}
