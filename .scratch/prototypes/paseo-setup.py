#!/usr/bin/env python3
# Prototype of `conductor paseo setup`: idempotently merge ONLY conductor-owned
# provider entries into paseo's config. Never touches the operator's own entries.
# Ownership is marked with "x-conductor": true so we never adopt/clobber a
# same-named operator entry we didn't create.
#
# PROVEN in the spike (against a COPY of ~/Projects/paseo-home/config.json):
#   run 1 -> added both; run 2 -> unchanged; run 3 (after drift) -> updated;
#   a same-named operator entry -> SKIP (not clobbered); all operator providers preserved.
#
# The real Go implementation lives in cmd/conductor (`conductor paseo setup`); it
# must additionally run `paseo reload` after a write and report added/updated/
# unchanged/skipped counts. NEVER run against the real config in tests.
import json, sys, copy
cfg_path = sys.argv[1]
OWNED = {
  "claude-sandboxed": {"extends":"claude","label":"Claude (sandboxed)","x-conductor":True,
     "command":["conductor","sandbox","exec","--","/home/daniel/.local/bin/claude-teamclaude"]},
  "codex-sandboxed": {"extends":"codex","label":"Codex (sandboxed)","x-conductor":True,
     "command":["conductor","sandbox","exec","--","codex"]},
  # pi is a built-in paseo provider; add a pi-sandboxed entry only where paseo
  # reports `pi` available. The extends+command mechanism was proven for codex
  # (also built-in), so pi is expected to work the same once installed.
}
cfg = json.load(open(cfg_path))
provs = cfg.setdefault("agents",{}).setdefault("providers",{})
added=updated=unchanged=0; skipped=[]
for name, want in OWNED.items():
    cur = provs.get(name)
    if cur is None:
        provs[name]=copy.deepcopy(want); added+=1; print(f"added     {name}")
    elif not cur.get("x-conductor"):
        skipped.append(name); print(f"SKIP      {name} (operator-owned, not touched)")
    elif cur==want:
        unchanged+=1; print(f"unchanged {name}")
    else:
        provs[name]=copy.deepcopy(want); updated+=1; print(f"updated   {name}")
json.dump(cfg, open(cfg_path,"w"), indent=2)
print(f"== added={added} updated={updated} unchanged={unchanged} skipped={len(skipped)} ==")
