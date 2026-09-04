package handoff

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/controller"
)

func TestHandlerPermissionApprove(t *testing.T) {
	ch := &scriptChannel{decisions: []Decision{{Action: ActionApprove}}}
	h := NewHandler(ch, nil)
	out, err := h.RequestPermission(context.Background(), controller.PermissionRequest{
		Tool:    "write_file",
		Detail:  "write internal/x.go",
		Options: []string{"allow-once", "allow-always", "reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Approved {
		t.Fatal("approve must grant the permission")
	}
	if out.Selected != "allow-once" {
		t.Fatalf("approve should select the first offered option, got %q", out.Selected)
	}
}

func TestHandlerPermissionReject(t *testing.T) {
	ch := &scriptChannel{decisions: []Decision{{Action: ActionDiscard}}}
	h := NewHandler(ch, nil)
	out, err := h.RequestPermission(context.Background(), controller.PermissionRequest{
		Tool:    "run",
		Options: []string{"allow", "reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Approved {
		t.Fatal("discard must reject the permission")
	}
	if out.Selected != "" {
		t.Fatalf("a rejected permission selects nothing, got %q", out.Selected)
	}
}

func TestHandlerInput(t *testing.T) {
	ch := &scriptChannel{decisions: []Decision{{Action: ActionRevise, Text: "use the staging bucket"}}}
	h := NewHandler(ch, nil)
	out, err := h.RequestInput(context.Background(), controller.InputRequest{Prompt: "which bucket?"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Cancelled || out.Text != "use the staging bucket" {
		t.Fatalf("input answer should carry the reply text, got %+v", out)
	}
}

func TestHandlerInputCancel(t *testing.T) {
	ch := &scriptChannel{decisions: []Decision{{Action: ActionDiscard}}}
	h := NewHandler(ch, nil)
	out, err := h.RequestInput(context.Background(), controller.InputRequest{Prompt: "?"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Cancelled {
		t.Fatal("discard must cancel the input request")
	}
}

// notifyRef records the presentation ref so we can assert the caller is told
// where the decision is waiting.
func TestHandlerNotifiesRef(t *testing.T) {
	ch := &scriptChannel{decisions: []Decision{{Action: ActionApprove}}}
	var seen string
	h := NewHandler(ch, func(ref string) { seen = ref })
	if _, err := h.RequestInput(context.Background(), controller.InputRequest{Prompt: "?"}); err != nil {
		t.Fatal(err)
	}
	if seen != "script://draft" {
		t.Fatalf("notify should receive the presentation ref, got %q", seen)
	}
}
