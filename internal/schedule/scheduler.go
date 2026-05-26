package schedule

import (
	"context"
	"time"

	"github.com/dongping/mateway/internal/config"
)

const defaultSchedulerInterval = 5 * time.Minute

type Scheduler struct {
	Config   *config.Root
	Runner   Runner
	Interval time.Duration
}

func NewScheduler(cfg *config.Root, handler Handler, policyHandlers ...PolicyHandler) Scheduler {
	home := ""
	if cfg != nil {
		home = cfg.App.Home
	}
	var policyHandler PolicyHandler
	if len(policyHandlers) > 0 {
		policyHandler = policyHandlers[0]
	}
	return Scheduler{
		Config:   cfg,
		Runner:   Runner{Store: NewStore(home), Handle: handler, PolicyHandler: policyHandler},
		Interval: defaultSchedulerInterval,
	}
}

func (s Scheduler) Start(ctx context.Context) {
	if s.Config == nil || !s.Config.Scheduler.Enabled {
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
	_, err := s.Runner.RunDue(context.Background(), now)
	return err
}
