package gateway

import (
	"context"
	"sync"
	"time"
)

// Step is one scripted model response.
type Step struct {
	Delay         time.Duration
	Content       string
	ModelReturned string
	Err           error
}

// Fake is a deterministic scripted Client for tests.
type Fake struct {
	mu      sync.Mutex
	Scripts map[string][]Step
	Calls   []ChatRequest
	taken   map[string]int
}

// NewFake builds a Fake with the given per-model scripts.
func NewFake(scripts map[string][]Step) *Fake {
	if scripts == nil {
		scripts = map[string][]Step{}
	}
	return &Fake{Scripts: scripts, taken: map[string]int{}}
}

// SetScript replaces the script for a model.
func (f *Fake) SetScript(model string, steps []Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scripts[model] = steps
}

// Chat replays the next scripted step for req.Model and records the call.
// Delay respects ctx cancellation.
func (f *Fake) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	steps := f.Scripts[req.Model]
	i := f.taken[req.Model]
	f.taken[req.Model] = i + 1
	f.mu.Unlock()

	if i >= len(steps) {
		return ChatResponse{ModelReturned: req.Model}, nil
	}
	step := steps[i]
	if step.Delay > 0 {
		timer := time.NewTimer(step.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	if step.Err != nil {
		return ChatResponse{ModelReturned: step.ModelReturned}, step.Err
	}
	model := step.ModelReturned
	if model == "" {
		model = req.Model
	}
	return ChatResponse{ModelReturned: model, Content: step.Content}, nil
}

// CallCount returns the number of recorded calls.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
