/* The real config schema, as conductor's loader decodes it (strict: an
 * unknown key is an error). Taken from internal/config on main
 * (connectors.go Step/TriggerSpec/Policy/BudgetPolicy/IsolationConfig,
 * config.go SkillPolicy) and feat/members (bots.go, teams.go) at the time of
 * writing. The prototype's validator uses this so "valid · checked on a copy"
 * only appears for config the real loader would accept. Keys the dashboard
 * design proposes but conductor doesn't have yet are listed in NEW and are
 * reported as such, never silently accepted. */
(function () {
  const set = (s) => new Set(s.split(/\s+/).filter(Boolean));
  const RS = (window.RS = {
    trigger: set('on name extends abstract enabled filter group steps hooks policy options repo shadow gate callable description inputs outputs'),
    workflow: set('name extends description inputs outputs steps gate hooks'),
    step: set(`id name if type agent model runtime prompt checkout output_schema background handoff repo images branch detach
      command workdir env use run code args host ssh uses options call workflow import with sleep log set assert fail wait_for
      watch idle_timeout for_each parallel retry timeout continue_on_error expect_push escalate_to compensate hooks decide gate
      team backend thinking mode workspace wait_timeout archive_when_done labels guidance memory session skill isolation trust
      outcome_feedback outcome_key bot browser <<`),
    hook: set('at id if uses options'),
    hookAt: ['start', 'done', 'fail', 'stop'],
    policy: set('quiet_hours concurrency max_fan_out ignore rate_limits backoff pause_label reply_to_bots shadow max_attempts_per_head agent_authored budget guidance'),
    budget: set('window max_cost_usd max_tokens'),
    isolation: set('mode user container limits privileged allow_root network fs'),
    isolationModes: ['user', 'namespace', 'container'],
    container: set('image engine'),
    limits: set('memory cpu pids'),
    network: set('egress deny'),
    skill: set('verbs secrets_via allow_secrets max_calls'),
    secretsVia: ['none', 'broker', 'env'],
    retry: set('max backoff while_output_matches interval timeout'),
    group: set('key window max'),
    // github's filter match keys (Configuration.md); each also legal as not_<key>; expr / not_expr anywhere
    filterKeys: { github: ['repo', 'branch', 'base_branch', 'title', 'label_any', 'label_all', 'require_label', 'author', 'comment_author', 'author_bot', 'draft', 'sole_assignee', 'merge_state', 'review_decision', 'non_author_approval', 'threads_resolved', 'check_name'] },
    boolFilterKeys: new Set(['draft', 'author_bot', 'sole_assignee', 'non_author_approval', 'threads_resolved']),
    // proposed with the dashboard (docs/design/dashboard.md), not in conductor yet
    NEW: { trigger: { folder: 'workflow folders (organization only)' }, workflow: { folder: 'workflow folders (organization only)' }, pack: { folders: 'placing pack workflows in folders' } },
    // bots and teams (feat/members)
    bot: set('description charter charter_file scope escalation operators memory_repo seed_dir home worker skill autonomy disabled terraform proactive peers team folder collections isolation browser'),
    team: set('description folder collections operators peers delegation autonomy budget isolation credentials browser knowledge routing'),
    teamCeilings: ['observe', 'reversible'],
    tiers: ['observe', 'reversible', 'propose', 'commit_money', 'never'],
  });
  /** The base key of a filter key: not_draft → draft. */
  RS.filterBase = (k) => String(k).replace(/^not_/, '');
  /** Filter keys an event accepts: its declared keys (with not_ stripped) plus not_ forms and expr. */
  RS.filterKeysFor = (declared) => { const base = [...new Set((declared || []).map(RS.filterBase))]; return base.flatMap((k) => [k, 'not_' + k]).concat(['expr', 'not_expr']); };
  /** Unknown keys of a map against a key set, minus proposed ones (returned separately). */
  RS.unknown = (obj, keys, newKeys = {}) => { const bad = [], proposed = []; Object.keys(obj || {}).forEach((k) => { if (keys.has(k)) return; if (newKeys[k]) proposed.push(k); else bad.push(k); }); return { bad, proposed }; };
  /** A go-yaml strict-decode style message. */
  RS.notFound = (line, key, type) => `line ${line}: field ${key} not found in type config.${type}`;
})();
