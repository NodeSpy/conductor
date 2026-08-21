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

// RerequestReviewGuidance is appended (when rerequest_review is set) so the agent
// closes the review loop after addressing feedback.
const RerequestReviewGuidance = "\n\n---\n" +
	"After you have addressed the feedback and pushed, RE-REQUEST review so the " +
	"loop continues. Identify the human reviewer(s) who requested changes " +
	"(`gh pr view {{.repo}}#{{.pr}} --json reviewRequests,reviews`) and re-request " +
	"each of them as yourself (skip bots):\n" +
	"  GH_TOKEN=$" + envGHWriteToken + " gh pr edit {{.repo}}#{{.pr}} --add-reviewer <login>\n" +
	"Only do this once your push has succeeded."
