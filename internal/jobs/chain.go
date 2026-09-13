package jobs

import (
	"errors"

	"github.com/dns/ggraphify/internal/gfy"
)

// ChainStep is one command in a chain.
type ChainStep struct {
	Kind   string
	Repo   string
	Label  string
	Params gfy.Params
	Env    gfy.Env
}

// ChainResult says how a chain ended.
type ChainResult struct {
	// Ran is how many steps completed successfully.
	Ran int
	// Total is how many the chain started with.
	Total int
	// Stopped is the step that failed or was cancelled, if one was.
	Stopped ChainStep
	// Err is why the chain stopped early. Nil when every step succeeded.
	Err error
}

// OK reports whether the whole chain ran.
func (c ChainResult) OK() bool { return c.Err == nil && c.Ran == c.Total }

// SubmitChain runs steps in order, submitting each one only after the previous
// has succeeded, and stopping at the first failure or cancellation.
//
// Sequencing has to happen here rather than by queueing everything at once,
// for two reasons that both bite in the Fix button's exact case:
//
//   - A later step's precondition is created by an earlier one. `cluster-only`
//     is refused when there is no graph.json, and after `extract` there is —
//     but only once it has finished. Submitted up front, it would be rejected
//     by the runner's precheck against the state of the world at click time.
//   - A failed step must not be followed by three more that build on it.
//     Labelling the communities of a graph whose rebuild just died is a bill
//     for a wrong answer.
//
// It returns immediately; done is called on a worker goroutine when the chain
// ends, and a GUI caller is responsible for hopping back to its own thread.
func (r *Runner) SubmitChain(steps []ChainStep, done func(ChainResult)) {
	if len(steps) == 0 {
		if done != nil {
			done(ChainResult{})
		}
		return
	}
	go func() {
		res := ChainResult{Total: len(steps)}
		for _, s := range steps {
			j, err := r.SubmitCmd(s.Kind, s.Repo, s.Label, s.Params, s.Env)
			if err != nil {
				res.Stopped, res.Err = s, err
				break
			}
			<-j.Done()

			r.mu.Lock()
			status := j.Status
			exitErr := j.Err
			r.mu.Unlock()

			if status != Succeeded {
				res.Stopped = s
				switch {
				case status == Canceled:
					res.Err = errors.New(gfy.Title(s.Kind) + " was cancelled")
				case exitErr != nil:
					res.Err = errors.New(gfy.Title(s.Kind) + " failed: " + exitErr.Error())
				default:
					res.Err = errors.New(gfy.Title(s.Kind) + " failed")
				}
				break
			}
			res.Ran++
		}
		if done != nil {
			done(res)
		}
	}()
}
