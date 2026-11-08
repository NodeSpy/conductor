package api

// The workflow builder's read models (design: js/screens/builder.js). They are
// what the builder needs beyond the workflow's own config node: what each
// configured connection can do (its verbs and events, read from the plugin's
// own declaration at runtime — core never lists them itself), the past events
// a test can be pointed at, and a manual run.

// WorkflowCatalogResponse is GET /api/v1/workflows/catalog: every configured
// connection with the verbs a step can call and the events a trigger can
// listen to, as its plugin declares them.
type WorkflowCatalogResponse struct {
	Connections []CatalogConnection `json:"connections"`
}

// CatalogConnection is one configured connection (connectors.<name>) and its
// plugin's declaration. Unavailable says why it can't describe itself yet (its
// plugin isn't installed); Verbs and Events are empty then.
type CatalogConnection struct {
	Name        string         `json:"name"`
	Use         string         `json:"use"`
	Desc        string         `json:"desc,omitempty"`
	Unavailable string         `json:"unavailable,omitempty"`
	Verbs       []CatalogVerb  `json:"verbs"`
	Events      []CatalogEvent `json:"events"`
}

// CatalogVerb is one verb: its options (the step's options: form) and the
// names of the outputs later steps can read.
type CatalogVerb struct {
	Name    string         `json:"name"`
	Desc    string         `json:"desc,omitempty"`
	Ask     bool           `json:"ask,omitempty"`
	Open    bool           `json:"open,omitempty"`
	Options []CatalogField `json:"options,omitempty"`
	Outputs []CatalogField `json:"outputs,omitempty"`
}

// CatalogEvent is one event a trigger's on: can name: the filter keys its
// filter: accepts and the fields a run started by it carries.
type CatalogEvent struct {
	Name    string         `json:"name"`
	Desc    string         `json:"desc,omitempty"`
	Dynamic bool           `json:"dynamic,omitempty"`
	Filters []string       `json:"filters,omitempty"`
	Fields  []CatalogField `json:"fields,omitempty"`
}

// WorkflowEvent is one past event a workflow's test can be pointed at: a run
// in the history that started from one of the workflow's own sources, with the
// fields it carried (tokens already stripped when the run was recorded).
type WorkflowEvent struct {
	ID     string         `json:"id"` // the run history id it came from
	On     string         `json:"on"`
	At     string         `json:"at"` // RFC3339
	Label  string         `json:"label"`
	Fields map[string]any `json:"fields,omitempty"`
}

// WorkflowBuilderInfo is GET /api/v1/workflows/{name}/builder: where the
// workflow lives in the config file (the path a save stages at; empty when it
// can't be edited here), what kind it is, and the past events a test can use.
type WorkflowBuilderInfo struct {
	// Kind is "trigger" (a triggers: entry: on: an event) or "workflow" (a
	// reusable workflows: entry, run with call:).
	Kind string `json:"kind"`
	// Path is the config path of its node: ["triggers","<index>"] or
	// ["workflows","<name>"]. Empty for one a pack or a bot owns.
	Path []string `json:"path,omitempty"`
	// File is the config file it lives in.
	File   string          `json:"file,omitempty"`
	Events []WorkflowEvent `json:"events"`
}

// WorkflowRunRequest is POST /api/v1/workflows/{name}/run: a manual run with
// these inputs (a reusable workflow's inputs:, or none).
type WorkflowRunRequest struct {
	Inputs map[string]any `json:"inputs,omitempty"`
}
