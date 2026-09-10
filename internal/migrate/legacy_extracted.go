package migrate

import "fmt"

// Legacy integration types this binary NO LONGER BUNDLES.
//
// sentry and pagerduty left core and are delivered as external plugins from the
// conductor-plugins repo. `conductor migrate` reads a user's OLD config, so it
// still RECOGNISES these types — but it deliberately does NOT transform them
// into connectors, because the plugin's contract is not the bundled
// connector's:
//
//   - Event names differ. The bundled sentry connector had ONE event, `alert`,
//     covering the issue/error/event_alert resources; the plugin declares three
//     (`issue_alert`, `error_alert`, `event_alert`). A migrated
//     `on: errors.alert` would name an event nothing emits.
//
//   - Context shape differs. The bundled integration emitted a NESTED map, so
//     steps templated `{{.sentry.title}}`; the plugin emits flat keys plus
//     filter-key aliases. A migrated prompt would render empty.
//
//   - `exclude:` is not evaluated. The bundled connectors had type-specific
//     filter functions that understood the exclusion maps the migration
//     generated to preserve legacy first-match-wins precedence. Plugin sources
//     go through the generic filter evaluator (internal/connector.filterMatch),
//     which requires every filter key to be present in the event context — so
//     an `exclude:` key makes the trigger match NOTHING.
//
// Transforming anyway would emit a config that looks migrated but whose
// triggers never fire and whose prompts render empty. So the migration SKIPS
// the integration and says so, loudly, and migrates the rest of the file
// normally — a legacy config with github + slack + sentry still migrates its
// github and slack halves.
var extractedTypes = map[string]string{
	"sentry":    "sentry",
	"pagerduty": "pagerduty",
}

// extractedNote is the mapping-summary line for a skipped integration. It is
// deliberately specific: the operator has to redo this one by hand, so it names
// the plugin, the install step, and each way the plugin's contract differs.
func extractedNote(typ, name string) string {
	comp := extractedTypes[typ]
	return fmt.Sprintf(
		"%s[%s]: NOT migrated — %s is no longer bundled in conductor; it is an external plugin. "+
			"Install it (plugins: { %s: { source: github.com/NodeSpy/conductor-plugins//%s, kind: connector } } "+
			"then `conductor init`) and write the connector + triggers by hand: the plugin's contract differs from "+
			"the bundled connector's (event names, flat context keys instead of {{.%s.*}}, and no exclude: support "+
			"for legacy first-match-wins rule precedence). Your original file is backed up. "+
			"See https://github.com/NodeSpy/conductor/wiki/Plugins",
		typ, name, typ, comp, comp, typ)
}
