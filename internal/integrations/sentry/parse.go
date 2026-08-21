package sentry

import "encoding/json"

// facts is the normalized subset of a Sentry alert we route + template on.
type facts struct {
	Resource    string
	Action      string
	Title       string
	Level       string
	Environment string
	Culprit     string
	ShortID     string
	Project     string
	URL         string
}

// wire mirrors the parts of a Sentry Integration-Platform webhook we read. The
// payload nests the interesting object under data.{issue|error|event} depending on
// the resource; we try each so `issue`, `error`, and `event_alert` all normalize.
type wire struct {
	Action string `json:"action"`
	Data   struct {
		Issue *sentryIssue   `json:"issue"`
		Error map[string]any `json:"error"`
		Event map[string]any `json:"event"`
	} `json:"data"`
}

type sentryIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Permalink string `json:"permalink"`
	Level     string `json:"level"`
	Project   struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"project"`
}

// parse extracts normalized facts from a Sentry webhook body. resource comes from
// the Sentry-Hook-Resource header (may be "").
func parse(resource string, body []byte) facts {
	var w wire
	_ = json.Unmarshal(body, &w)
	f := facts{Resource: resource, Action: w.Action}

	if w.Data.Issue != nil {
		i := w.Data.Issue
		f.Title, f.Culprit, f.Level = i.Title, i.Culprit, i.Level
		f.ShortID, f.URL, f.Project = i.ShortID, i.Permalink, i.Project.Slug
		return f
	}
	// error / event_alert resources carry a flat-ish event map.
	m := w.Data.Error
	if m == nil {
		m = w.Data.Event
	}
	if m != nil {
		f.Title = firstNonEmpty(gs(m, "title"), gs(m, "message"))
		f.Level = gs(m, "level")
		f.Environment = gs(m, "environment")
		f.Culprit = gs(m, "culprit")
		f.ShortID = firstNonEmpty(gs(m, "issue_id"), gs(m, "event_id"))
		f.URL = firstNonEmpty(gs(m, "web_url"), gs(m, "url"), gs(m, "issue_url"))
		f.Project = projectSlug(m)
	}
	return f
}

// gs reads a string field from a decoded JSON map (missing/non-string → "").
func gs(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// projectSlug pulls a project slug from either a nested object or a flat string.
func projectSlug(m map[string]any) string {
	switch p := m["project"].(type) {
	case string:
		return p
	case map[string]any:
		return gs(p, "slug")
	}
	return ""
}
