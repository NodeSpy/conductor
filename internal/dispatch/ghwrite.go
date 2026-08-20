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

// HoldMarker is the filename an agent creates in its working directory to signal
// it still needs the user, so the reaper won't archive it while it waits.
const HoldMarker = ".paseo-hold"

// HoldGuidance is appended to archive-when-done agent prompts so an agent that
// needs the user can keep itself alive instead of being culled when it goes idle.
const HoldGuidance = "\n\n---\n" +
	"You are auto-archived when you go idle. If you still need input or a decision " +
	"from me before you can finish, keep yourself alive by creating a hold marker in " +
	"your working directory: `touch " + HoldMarker + "` (do NOT commit it). " +
	"Remove it (`rm -f " + HoldMarker + "`) once you no longer need me and are done."

// RerequestReviewGuidance is appended (when rerequest_review is set) so the agent
// closes the review loop after addressing feedback.
const RerequestReviewGuidance = "\n\n---\n" +
	"After you have addressed the feedback and pushed, RE-REQUEST review so the " +
	"loop continues. Identify the human reviewer(s) who requested changes " +
	"(`gh pr view {{.repo}}#{{.pr}} --json reviewRequests,reviews`) and re-request " +
	"each of them as yourself (skip bots):\n" +
	"  GH_TOKEN=$" + envGHWriteToken + " gh pr edit {{.repo}}#{{.pr}} --add-reviewer <login>\n" +
	"Only do this once your push has succeeded."
