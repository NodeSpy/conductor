/* The real config schema, as conductor's loader decodes it (strict: an
 * unknown key is an error). Taken from internal/config on main
 * (connectors.go Step/TriggerSpec/Policy/BudgetPolicy/IsolationConfig,
 * config.go SkillPolicy), feat/members @ 47cb1186 (bots.go, teams.go,
 * botidentity.go: a bot's identity) and feat/agent-browser @ f129b72b (the
 * browser, the wallet and purchase limits). Re-sync when those move. The prototype's validator uses this so "valid · checked on a copy"
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
    NEW: { trigger: {}, workflow: {}, pack: {} },
    // bots and teams: BotConfig with its inline Identity (avatar, color, display_name, tagline, personality) and
    // TeamConfig (feat/members botidentity.go, bots.go, teams.go); purchases: a bot's purchase caps and a team's
    // budget.purchases (feat/agent-browser bots.go, teams.go, "Spend limits stack")
    bot: set(`description charter charter_file scope escalation operators memory_repo seed_dir home worker skill autonomy
      disabled terraform proactive peers team folder collections use connectors grant memory attachments isolation browser
      purchases avatar color display_name tagline personality`),
    team: set('description folder collections operators peers delegation autonomy budget isolation credentials browser knowledge routing chat avatar'),
    teamBudget: set('spend_cap proactive purchases'),
    browserCfg: set('allow chrome evaluate idle_timeout max_browsers max_download_mb max_upload_mb logins credentials container container_engine mailboxes profiles cards payments'),
    // an avatar (BotAvatar, both bots and teams): a face { shape, eyes?, color? } (bots only), an icon with a colour, or
    // { image: <png|jpeg|webp file beside the config> }; a deprecated preset reads as a shape and eyes; a team with no
    // avatar shows its initials (there is no initials key)
    avatar: set('shape eyes icon color preset image'),
    personality: set('tone style'),
    identityCaps: { display_name: 40, tagline: 120, style: 600 },
    // conductor's name lists today (botidentity.go). Its own comment: "placeholders until the designed set lands — changing
    // a list is a one-file change". The dashboard draws the brand's designed set (js/mascots.js: 13 shapes, 8 eye sets, the
    // identity colours in css), so a name off these lists is reported as the brand's, not as an error
    lists: {
      shapes: ['circle', 'squircle', 'hexagon', 'shield', 'blob', 'diamond', 'drop', 'star'],
      eyes: ['round', 'wide', 'sleepy', 'wink', 'visor', 'dots', 'happy', 'focused'],
      icons: ['radar', 'shield', 'wrench', 'book', 'bolt', 'eye', 'gear', 'pulse', 'cloud', 'database', 'terminal', 'chart', 'bell', 'key', 'globe'],
      colors: ['indigo', 'violet', 'plum', 'magenta', 'teal', 'ocean', 'slate', 'sand', 'graphite'],
      statusHues: ['red', 'green', 'amber', 'yellow', 'orange'],
      tones: ['terse', 'friendly', 'formal', 'warm', 'dry', 'playful'],
    },
    teamCeilings: ['observe', 'reversible', 'commit_money'], // commit_money: a team's bots may buy unattended (feat/agent-browser)
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
