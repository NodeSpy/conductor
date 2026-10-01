package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// launchFieldsDispatcher is a fakeDispatcher that declares it carries the
// launch fields, the way *dispatch.Dispatcher does.
type launchFieldsDispatcher struct{ *fakeDispatcher }

func (launchFieldsDispatcher) HonorsLaunchFields() bool { return true }

// TestLaunchFieldsNeedHonoringRunner: a step with detach:/repo:/images: is
// refused by a runner that does not declare HonorsLaunchFields — an ignored
// detach: would otherwise launch an owned, credentialed agent — and passes
// through to one that does.
func TestLaunchFieldsNeedHonoringRunner(t *testing.T) {
	for _, step := range []config.Step{{Detach: true}, {Repo: "a/b"}, {Images: []string{"/x.png"}}} {
		plain := &fakeDispatcher{}
		e, _ := newEng(t, baseCfg(), plain, &fakeNotifier{}, nil)
		_, err := e.flowAgentServices().Dispatch(context.Background(), dispatch.Request{
			Action: config.Action{Type: "agent", Prompt: "p"}, Step: step,
		})
		if err == nil || !strings.Contains(err.Error(), "builtin paseo runtime") {
			t.Fatalf("step %+v: want a refusal, got %v", step, err)
		}
		if len(plain.reqs) != 0 {
			t.Fatalf("step %+v: a refused launch must not reach the runner", step)
		}

		ok := launchFieldsDispatcher{&fakeDispatcher{}}
		e2 := New(Options{Config: baseCfg(), Store: tempStore(t), Dispatch: ok, Notifier: &fakeNotifier{}})
		if _, err := e2.flowAgentServices().Dispatch(context.Background(), dispatch.Request{
			Action: config.Action{Type: "agent", Prompt: "p"}, Step: step,
		}); err != nil {
			t.Fatalf("step %+v: honoring runner refused: %v", step, err)
		}
		if len(ok.reqs) != 1 {
			t.Fatalf("step %+v: honoring runner not called", step)
		}
	}
}
