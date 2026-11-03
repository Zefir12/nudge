package gitstacks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	pollTick = 15 * time.Second
	// maxPollsAtOnce bounds the repositories looked at, and stacks deployed, at once.
	maxPollsAtOnce = 4
)

// Due lists the sources whose poll interval has passed.
func (s *Service) Due(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceColumns+` FROM stack_sources
		WHERE poll_seconds > 0 AND checked_at + poll_seconds <= ? ORDER BY checked_at`, s.now().Unix())
	if err != nil {
		return nil, fmt.Errorf("list due stack sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var due []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("list due stack sources: %w", err)
		}
		due = append(due, src)
	}
	return due, rows.Err()
}

func (s *Service) checked(ctx context.Context, src Source) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stack_sources SET checked_at = ? WHERE environment_id = ? AND stack = ?`,
		s.now().Unix(), src.Environment, src.Stack)
	return err
}

// StartPolling sets deploy, which gets a source whose ref moved past the commit last seen,
// and runs polling until the Service's Context ends (without one, nothing polls).
func (s *Service) StartPolling(deploy func(ctx context.Context, src Source)) {
	s.deploy = deploy
	if s.ctx == nil {
		return
	}
	s.pollOnce.Do(func() {
		go func() {
			t := time.NewTicker(pollTick)
			defer t.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-t.C:
					s.PollOnce(s.ctx)
				}
			}
		}()
	})
}

// PollOnce looks at every due source and returns when done.
func (s *Service) PollOnce(ctx context.Context) {
	due, err := s.Due(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("poll stack sources", "err", err)
		}
		return
	}
	slots := make(chan struct{}, maxPollsAtOnce)
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, src := range due {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Go(func() {
			defer func() { <-slots }()
			s.Check(ctx, src)
		})
	}
}

// Check looks at one source, deploying it if its ref moved.
func (s *Service) Check(ctx context.Context, src Source) {
	if err := s.checked(ctx, src); err != nil {
		s.log.Warn("poll stack source", "env", src.Environment, "stack", src.Stack, "err", err)
		return
	}
	head, err := s.Head(ctx, src)
	if err != nil {
		s.log.Warn("poll stack source", "env", src.Environment, "stack", src.Stack, "err", err)
		var ge *Error
		msg := "could not read the repository"
		if errors.As(err, &ge) {
			msg = ge.Msg
		}
		if err := s.RecordFailure(context.WithoutCancel(ctx), src.Environment, src.Stack, "", msg); err != nil {
			s.log.Warn("poll stack source", "env", src.Environment, "stack", src.Stack, "err", err)
		}
		return
	}
	if head != src.SeenCommit && s.deploy != nil {
		s.deploy(ctx, src)
	}
}
