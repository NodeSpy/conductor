package dispatch

// Token identity model for dispatched work:
//
//	GH_TOKEN / GITHUB_TOKEN = YOUR token → the default for everything an agent or
//	command does, so all writes (comments, reviews, replies, any gh/API call) are
//	attributed to YOU, never the App/bot. Commits and `git push` go over SSH as you.
//
//	PC_GH_APP_TOKEN = the App installation token, exposed ONLY for optional
//	rate-limited READS — it must never be used to post/submit anything.
//
// envGHWriteToken is kept as an alias of your token for backward compatibility with
// any prompt still referencing it; GH_TOKEN is now the same token, so it's redundant.
const (
	envGHWriteToken = "PC_GH_WRITE_TOKEN"
	envGHAppToken   = "PC_GH_APP_TOKEN"
)

// WriteWrapperGuidance is appended to agent prompts so the agent knows its GitHub
// identity IS you — writing as the App bot must never happen.
const WriteWrapperGuidance = "\n\n---\n" +
	"IDENTITY: you act as ME. GH_TOKEN/GITHUB_TOKEN are MY token, so every comment, " +
	"review, reply, and `gh`/API write is attributed to me — and commits and `git push` " +
	"go over SSH as me. NEVER post, submit, approve, or otherwise write anything with the " +
	"App/bot token. If a large read would burn my rate limit you MAY read (only) with the " +
	"App token via `GH_TOKEN=$" + envGHAppToken + " gh ...`, but never write with it."

// BotReplyGuidance is appended to an agent prompt when the triggering
// comment/review was authored by a bot and the resolved reply_to_bots policy
// is decline_only (the default): a bot cannot read pleasantries, so the only
// reply worth posting is a concrete decline.
const BotReplyGuidance = "\n\n---\n" +
	"The comment you are responding to was posted by an automated bot, not a person. " +
	"Do not thank it, acknowledge it, or add pleasantries. Apply any valid fix directly. " +
	"Post a reply comment ONLY to state a concrete reason for not applying a suggestion, " +
	"and keep it terse."

// ConcisionGuidance is appended to every dispatched agent prompt so the text it
// posts to GitHub (and its own wrap-up) reads like a person, not an essay. Kept
// short on purpose.
const ConcisionGuidance = "\n\n---\n" +
	"TONE: be concise and human. Anything you post to GitHub — comments, replies, " +
	"review notes — should read like a busy engineer dashed it off: a sentence or " +
	"two, plain and direct. No preamble, no restating the question, no summarizing " +
	"what you did, no headers or bullet dumps unless they genuinely earn their place. " +
	"Say only what's needed and stop. If nothing needs saying, post nothing. Keep " +
	"your final wrap-up short too — a line, not an essay."

// HoldMarker is a legacy fallback: a file an agent may create in its working
// directory to signal it still needs the user. The reaper still honors the marker
// if present, for contexts where asking an interactive question isn't possible.
const HoldMarker = ".paseo-hold"

// NOTE: autonomous agents (top-level fixers AND non-background workflow steps) are
// deliberately NOT told to ask — they make the best decision and finish. A schema
// step like `assess` that pauses to ask fails to produce its structured output.
// Only the interactive hand-off (a background step) is told to ask; see
// HandoffGuidance. (There used to be a "HoldGuidance" that nudged archive-when-done
// agents to ask "only if they need me"; it caused exactly that assess failure and
// is retired.)

// HandoffGuidance is appended to every background (hand-off) step. A hand-off
// exists to bring a decision to the user, so it must ALWAYS finish by asking rather
// than going idle. Ending idle shows up in paseo as merely "ready" and is easy to
// miss; asking surfaces as "needs your input" (and pauses the agent, keeping its
// workspace alive).
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
	"carefully. Check current review state (you act as me — plain gh):\n" +
	"  gh pr view {{.repo}}#{{.pr}} --json reviews,reviewRequests\n" +
	"Re-request review ONLY from human reviewer(s) whose LATEST review state is " +
	"CHANGES_REQUESTED and who are not already a pending requested reviewer:\n" +
	"  gh pr edit {{.repo}}#{{.pr}} --add-reviewer <login>\n" +
	"Do NOT re-request anyone whose latest review is APPROVED (even approve-with-nits) — " +
	"that would dismiss their approval. If NO reviewer currently has changes-requested " +
	"outstanding, do nothing here and do not ask me about it; the loop is already closed. " +
	"Only re-request once your push has succeeded, and skip bots."
