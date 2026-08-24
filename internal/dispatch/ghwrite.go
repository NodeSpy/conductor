package dispatch

// envGHWriteToken is the environment variable a dispatched agent uses to post
// to GitHub *as you* (comments, review replies), while its default GH_TOKEN
// (the App installation token) handles reads on the App's rate pool.
//
// A single GH_TOKEN can't be both, so the split is a convention the agent
// prompt references: reads use the ambient `gh`; to post, run
//
//	GH_TOKEN=$PC_GH_WRITE_TOKEN gh pr comment ...
//
// git push needs no token at all (it goes over SSH as you).
const envGHWriteToken = "PC_GH_WRITE_TOKEN"

// WriteWrapperGuidance is appended to agent prompts so the agent knows how to
// post as you rather than as the App bot.
const WriteWrapperGuidance = "\n\n---\n" +
	"To post any GitHub comment/reply, run it as yourself with your write token:\n" +
	"  GH_TOKEN=$" + envGHWriteToken + " gh pr comment/review ...\n" +
	"Reads (gh pr diff, gh run view, gh pr checks) use the ambient GH_TOKEN. " +
	"Commit and `git push` normally — pushes go over SSH as you."

// HoldMarker is a legacy fallback: a file an agent may create in its working
// directory to signal it still needs the user. The primary mechanism is now an
// interactive question (see HoldGuidance); the reaper still honors the marker if
// present, for contexts where asking a question isn't possible.
const HoldMarker = ".paseo-hold"

// HoldGuidance is appended to archive-when-done agent prompts. An agent that needs
// the user must ASK an interactive question — which pauses it and surfaces as a
// pending permission the reaper spares — rather than finishing with the question
// written as plain text (which just goes idle and gets archived before I see it).
const HoldGuidance = "\n\n---\n" +
	"IMPORTANT — how to reach me: you run unattended and are archived once you go idle. " +
	"If you need my input, a decision, or my attention — or you have a question — do NOT " +
	"stop and write the question as plain text (I won't see it and you'll be archived). " +
	"Instead ASK me using your interactive multiple-choice question tool (AskUserQuestion) " +
	"and WAIT for my answer: that pauses you and keeps your workspace alive until I " +
	"respond. Only finish when you are genuinely done and need nothing from me."

// HandoffGuidance is appended to every background (hand-off) step. Unlike a plain
// archive-when-done agent — which asks only IF it needs something (HoldGuidance) —
// a hand-off exists to bring a decision to the user, so it must ALWAYS finish by
// asking rather than going idle. Ending idle shows up in paseo as merely "ready"
// and is easy to miss; asking surfaces as "needs your input" (and pauses the agent,
// keeping its workspace alive).
const HandoffGuidance = "\n\n---\n" +
	"HAND-OFF — this run is for ME to decide on. You do the work, then bring it to me " +
	"as a decision; I'll drive you interactively in paseo. When you finish the work " +
	"(e.g. you've drafted the review), do NOT stop or \"wait\" with your result written " +
	"as plain text — that shows up only as \"ready\" and I may miss it. You MUST conclude " +
	"by ASKING me with your interactive multiple-choice question tool (AskUserQuestion): " +
	"briefly summarize what you produced and offer clear next-step choices (for a review, " +
	"e.g. post as-is / revise / discard), then WAIT for my answer. Do this every time you " +
	"need me — including after each revision — so I'm always alerted. Never end your turn " +
	"idle while you still need a decision from me."

// RerequestReviewGuidance is appended (when rerequest_review is set) so the agent
// closes the review loop after addressing feedback — but ONLY when there's an
// actual changes-requested review to re-request. It must never dismiss a standing
// approval (re-requesting a reviewer who approved wipes their approval), and must
// not ask what to do when there's simply no target.
const RerequestReviewGuidance = "\n\n---\n" +
	"AFTER you have addressed the feedback and pushed, close the review loop — but " +
	"carefully. Check current review state:\n" +
	"  GH_TOKEN=$" + envGHWriteToken + " gh pr view {{.repo}}#{{.pr}} --json reviews,reviewRequests\n" +
	"Re-request review ONLY from human reviewer(s) whose LATEST review state is " +
	"CHANGES_REQUESTED and who are not already a pending requested reviewer:\n" +
	"  GH_TOKEN=$" + envGHWriteToken + " gh pr edit {{.repo}}#{{.pr}} --add-reviewer <login>\n" +
	"Do NOT re-request anyone whose latest review is APPROVED (even approve-with-nits) — " +
	"that would dismiss their approval. If NO reviewer currently has changes-requested " +
	"outstanding, do nothing here and do not ask me about it; the loop is already closed. " +
	"Only re-request once your push has succeeded, and skip bots."
