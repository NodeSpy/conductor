#!/bin/bash
# Prototype of `conductor call git.push` — runs OUTSIDE the agent sandbox, as conductor.
# Policy: push the workspace HEAD to EXACTLY the dispatch branch, fast-forward only,
# re-signing the pushed commits as the operator. Refuses any other ref and any force.
#
# PROVEN in the spike (throwaway bare repo):
#   - HEAD -> refs/heads/<dispatch> only; agent cannot name another ref;
#   - fast-forward only: a diverged remote branch REJECTED the push (not forced), remote unchanged;
#   - every other ref refused: refs/heads/main, ../evil, "fix/42 --force", "", HEAD, a^b, x:y;
#   - signed as Daniel (SSH); `git log --show-signature` => G (Good) for the committer email.
#     That signature is exactly what GitHub reads for the "Verified" badge; the badge itself
#     needs conductor's signing key registered as an SSH *signing* key on the account
#     (do NOT touch account settings).
#
# The real Go implementation lives in the git connector (`git.push` verb) + an
# end-of-step push. It must sign the whole new range (remote..HEAD), not just HEAD.
set -euo pipefail
WS="$1"; DISPATCH_BRANCH="$2"; REMOTE="$3"; SIGNKEY="$4"; SIGNER_NAME="$5"; SIGNER_EMAIL="$6"
# --- guardrails ---
case "$DISPATCH_BRANCH" in
  ""|*' '*|*'~'*|*'^'*|*':'*|*'*'*|*'['*|*'\'*|refs/*|-*|HEAD) echo "REFUSE: illegal branch name '$DISPATCH_BRANCH'"; exit 2;;
esac
cd "$WS"
head=$(git rev-parse HEAD)
# Re-sign every commit the push would introduce, as the operator, with conductor's key.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
git config user.name "$SIGNER_NAME"; git config user.email "$SIGNER_EMAIL"
git config gpg.format ssh; git config user.signingkey "$SIGNKEY.pub"
git config commit.gpgsign true
# sign the single HEAD commit (prototype: amend to sign; a real impl signs the whole new range)
GIT_SSH_COMMAND=true git -c user.signingkey="$SIGNKEY.pub" commit --amend --no-edit -S >/dev/null
signed=$(git rev-parse --short HEAD)
# --- the ONLY push conductor performs: HEAD -> refs/heads/<dispatch>, no force, ff-only ---
git push --no-force-with-lease "$REMOTE" "HEAD:refs/heads/$DISPATCH_BRANCH"
echo "PUSHED $signed -> refs/heads/$DISPATCH_BRANCH (ff-only, signed as $SIGNER_EMAIL)"
