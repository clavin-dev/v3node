package task

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type Task struct {
	Name                   string
	Interval               time.Duration
	Execute                func() error
	Reload                 func()
	DisableReloadOnTimeout bool
	TimeoutReloadThreshold int
	Access                 sync.RWMutex
	Running                bool
	Stop                   chan struct{}
	execCancel             context.CancelFunc
	timeoutConsecutive     int
	loopWG                 sync.WaitGroup
}

func (t *Task) Start(first bool) error {
	t.Access.Lock()
	if t.Running {
		t.Access.Unlock()
		return nil
	}
	t.Running = true
	t.Stop = make(chan struct{})
	t.timeoutConsecutive = 0
	t.execCancel = nil
	t.Access.Unlock()

	t.loopWG.Add(1)
	go func() {
		defer t.loopWG.Done()
		timer := time.NewTimer(t.Interval)
		defer timer.Stop()
		if first {
			if err := t.ExecuteWithTimeout(); err != nil {
				t.safeStop()
				return
			}
		}

		for {
			timer.Reset(t.Interval)
			select {
			case <-timer.C:
				// continue
			case <-t.Stop:
				return
			}

			if err := t.ExecuteWithTimeout(); err != nil {
				log.Errorf("Task %s execution error: %v", t.Name, err)
				return
			}
		}
	}()

	return nil
}

func (t *Task) ExecuteWithTimeout() error {
	timeout := min(3*t.Interval, 5*time.Minute)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Access.Lock()
	if !t.Running {
		t.Access.Unlock()
		cancel()
		return nil
	}
	t.execCancel = cancel
	t.Access.Unlock()

	defer func() {
		t.Access.Lock()
		t.execCancel = nil
		t.Access.Unlock()
		cancel()
	}()

	done := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("Task %s panicked: %v\n%s", t.Name, r, debug.Stack())
				select {
				case done <- fmt.Errorf("panic: %v", r):
				default:
				}
			}
		}()
		select {
		case done <- t.Execute():
		default:
		}
	}()

	select {
	case <-ctx.Done():
		t.handleTimeout()
		return nil
	case err := <-done:
		t.Access.Lock()
		t.timeoutConsecutive = 0
		t.Access.Unlock()
		return err
	}
}

func (t *Task) handleTimeout() {
	t.Access.Lock()
	t.timeoutConsecutive++
	consecutive := t.timeoutConsecutive
	running := t.Running
	reload := t.Reload
	disableReload := t.DisableReloadOnTimeout
	threshold := t.TimeoutReloadThreshold
	taskName := t.Name
	t.Access.Unlock()

	if !running {
		return
	}
	if threshold <= 0 {
		threshold = 1
	}
	if disableReload || reload == nil {
		log.WithFields(log.Fields{
			"task":        taskName,
			"consecutive": consecutive,
		}).Warn("Task execution timed out")
		return
	}
	if consecutive < threshold {
		log.WithFields(log.Fields{
			"task":        taskName,
			"consecutive": consecutive,
			"threshold":   threshold,
		}).Warn("Task execution timed out, waiting before reload")
		return
	}

	t.Access.Lock()
	t.timeoutConsecutive = 0
	t.Access.Unlock()
	log.WithFields(log.Fields{
		"task":      taskName,
		"threshold": threshold,
	}).Error("Task execution timed out, reloading")
	reload()
}

func (t *Task) safeStop() {
	t.Access.Lock()
	if !t.Running {
		t.Access.Unlock()
		return
	}
	t.Running = false
	cancel := t.execCancel
	stop := t.Stop
	t.execCancel = nil
	t.Stop = nil
	t.timeoutConsecutive = 0
	t.Access.Unlock()

	if cancel != nil {
		cancel()
	}
	if stop != nil {
		close(stop)
	}
}

func (t *Task) Close() {
	t.safeStop()
	t.loopWG.Wait()
	log.Warningf("Task %s stopped", t.Name)
}
