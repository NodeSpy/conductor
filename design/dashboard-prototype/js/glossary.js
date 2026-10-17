/* Plain definitions for conductor's nouns. UI.term(text, key) renders a dotted
 * term that explains itself on hover, keyboard focus or tap; #/glossary lists
 * them all. One sentence each, in the words a newcomer would use. */
(function () {
  const G = (UI.GLOSSARY = {
    bot: ['Bot', 'An AI teammate with memory you can chat with. It watches what you pick, tells you what it finds, and proposes actions; risky ones wait for you.'],
    workflow: ['Workflow', 'A fixed recipe: when something happens, run these steps in order. No judgment unless a step asks an agent or hands the event to a bot.'],
    trigger: ['Trigger', 'The "when" of a workflow: the event it waits for (a failed check, an incident, a schedule) and which ones count.'],
    step: ['Step', 'One thing a workflow does: call a service, run a command, ask an agent, or hand the event to a bot.'],
    agent: ['Agent', 'An AI model run for one step with a prompt, in a sandbox. It finishes with the step; a bot is the one that remembers.'],
    team: ['Team', 'A group of bots that share approvers, limits and knowledge. A bot on a team can only be stricter than its team.'],
    folder: ['Folder', 'Organization only: where something shows in lists. Folders never grant or limit anything.'],
    pack: ['Pack', 'A bundle of workflows (and sometimes bots) someone else wrote, installed and updated as one unit.'],
    plugin: ['Plugin', 'Code that teaches conductor a new service or engine, such as a vendor connector.'],
    connector: ['Connector', 'Your connection to a service (GitHub, Slack, PagerDuty…): the account and the events it delivers.'],
    verb: ['Verb', 'One action a connector can take, named <instance>.<action>, like gh.comment or slack-ops.post.'],
    tier: ['Tier', 'How much a bot may do with a verb: observe (read only), reversible (can be undone), propose (asks you first), commit_money (spends money, always asks), never.'],
    observe: ['observe', 'Reads only. Nothing changes.'],
    reversible: ['reversible', 'Changes something that can be undone, like a comment or a post. Asks you first unless it is promoted.'],
    propose: ['propose', 'Asks you first: the bot proposes it and nothing happens until you approve.'],
    commit_money: ['commit_money', 'Spends real money. Always asks you, within the spend cap.'],
    never: ['never', 'Refused outright, even if you approve.'],
    promote: ['Promoted', 'A reversible verb you let the bot run without asking each time. A team ceiling can forbid it.'],
    ceiling: ['Ceiling', 'The most a team lets its bots be promoted to: reversible, or observe (no bot acts on its own).'],
    shadow: ['Shadow mode', 'The bot records what it would do, but nothing executes. Use it to watch a new bot before trusting it.'],
    requester: ['Requester', 'Who asked for the work. A bot never gets more trust than whoever asked it, through every hand-off (this is "provenance").'],
    provenance: ['Provenance', 'Where a request came from. A bot acts with no more trust than the person or event that started it.'],
    widening: ['Widening', 'A change that lets bots or agents do more: a new permission, host, repo or promotion. Widenings are listed before you apply.'],
    digest: ['Digest', 'A fingerprint of exactly what you approve. If anything changes after you sign, the approval no longer matches and is refused.'],
    case: ['Case', 'One piece of work a bot is handling, like a ticket: what started it, notes, proposals and the result.'],
    home: ['Home session', 'The bot\'s ongoing conversation. It is rotated (started fresh) after a number of turns or hours; memory carries over.'],
    turn: ['Turn', 'One reply from the bot\'s model. Counted to rotate sessions and to cap how much a bot may run.'],
    sweep: ['Sweep', 'A bot checking in on its own on a schedule, without an event.'],
    handoff: ['Hand-off', 'A step that pauses the run and asks a person to approve, revise or discard what an agent made.'],
    fleet: ['Fleet', 'An ordered list of acceptable models. A step starts on the first (rung 1) and moves down the list on retry.'],
    sandbox: ['Sandbox', 'Where agents run: they can only reach the files and network hosts you allowed.'],
    egress: ['Egress', 'Network hosts an agent may reach from its sandbox. Anything else is denied.'],
    vault: ['Vault', 'Where secrets live (1Password, pass, HashiCorp, conductor\'s own). Config holds a reference, never the value.'],
    staged: ['Staged', 'A config change made in the dashboard but not applied yet. Review the diff, then apply with your passkey.'],
    operator: ['Operator', 'A person who may approve what bots propose, with a passkey.'],
    synthesized: ['Synthesized', 'Made by conductor from a bot\'s definition. Edit the bot to change it.'],
    knowledge: ['Knowledge', 'Facts your bots read, with the evidence for each. Kept in git.'],
    runbook: ['Runbook', 'A step-by-step procedure your bots (and you) follow, with how to check it worked and how to undo it.'],
    collection: ['Collection', 'A custom kind of entry (say, postmortems) with its own folder and fields.'],
  });
  /** A dotted term with its definition; key defaults to the lower-cased text. */
  UI.term = (text, key) => { const k = key || String(text).toLowerCase().replace(/\s+/g, '_'); const d = G[k]; return d ? `<span class="term" tabindex="0" data-tip="<b>${UI.esc(d[0])}</b><br>${UI.esc(d[1])}" data-term="${k}">${text}</span>` : text; };
  // keyboard users get the same tip on focus
  document.addEventListener('focusin', (e) => { const el = e.target.closest && e.target.closest('.term'); if (el) el.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); });
  document.addEventListener('focusout', (e) => { if (e.target.closest && e.target.closest('.term')) document.body.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); });
  UI.route('glossary', () => `<div class="page wizard">${UI.head('Glossary', { sub: 'the words conductor uses, in plain language' })}
    <div class="panel"><dl class="gloss">${Object.entries(G).map(([k, [t, d]]) => `<div id="g-${k}"><dt>${UI.esc(t)}</dt><dd>${UI.esc(d)}</dd></div>`).join('')}</dl></div></div>`);
})();
