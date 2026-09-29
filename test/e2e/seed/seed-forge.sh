#!/usr/bin/env bash
# Seed the local bare-git forge and serve it over the git:// protocol. Each repo
# named in $REPOS gets a `main` branch, a `pr-1` branch with a divergent commit,
# and a `refs/pull/1/head` mirror (so the fake paseo's checkout-pr can fetch a real
# PR ref). Push is enabled so agents can push their fixes back. Fully hermetic.
set -euo pipefail

BASE=/srv/git
REPOS="${REPOS:-acme/web}"

git config --global user.email "seed@forge.test"
git config --global user.name "Forge Seed"
git config --global init.defaultBranch main
git config --global --add safe.directory '*'

seed_repo() {
  local repo="$1"
  local bare="$BASE/$repo.git"
  mkdir -p "$(dirname "$bare")"
  git init --bare -q "$bare"

  local tmp
  tmp="$(mktemp -d)"
  (
    cd "$tmp"
    git init -q
    git checkout -q -b main
    if [ "$repo" = grpx/jailpair ]; then
      # Per-dispatch clones (#154 B): a partial (blob:none) base clone, and
      # an old blob only the forge has — a dispatch clone fetches it lazily.
      git -C "$bare" config uploadpack.allowFilter true
      git -C "$bare" config uploadpack.allowAnySHA1InWant true
      echo "ancient history" > OLD.txt
      git add OLD.txt
      git commit -qm "old"
      git rm -q OLD.txt
    fi
    echo "# $repo" > README.md
    printf 'line 1\nline 2\nline 3\n' > app.txt
    git add -A
    git commit -qm "initial commit"
    git remote add origin "$bare"
    git push -q origin main

    # A PR branch that diverges from main (the "PR under test").
    git checkout -q -b pr-1
    printf 'line 1\nline 2 (pr change)\nline 3\n' > app.txt
    git commit -qam "pr-1: change app.txt"
    git push -q origin pr-1
    # Mirror it at the GitHub-style PR head ref.
    git push -q origin pr-1:refs/pull/1/head
    if [ "$repo" = grpx/jailpair ]; then
      git checkout -q -b pr-2 main
      echo two > two.txt
      git add two.txt
      git commit -qm "pr-2"
      git push -q origin pr-2 pr-2:refs/pull/2/head
    fi
  )
  rm -rf "$tmp"
  echo "seeded $bare (main, pr-1, refs/pull/1/head)"
}

for repo in $REPOS; do
  seed_repo "$repo"
done

echo "forge seeded; starting git daemon on :9418"
exec git daemon \
  --reuseaddr \
  --export-all \
  --enable=receive-pack \
  --base-path="$BASE" \
  --verbose \
  "$BASE"
