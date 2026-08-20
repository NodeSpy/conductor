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
