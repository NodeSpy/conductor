package pagerduty

import "encoding/json"

// facts is the normalized subset of a PagerDuty V3 incident event we route + template on.
type facts struct {
	EventType string // incident.triggered | incident.escalated | incident.resolved | …
	Status    string
	Title     string
	Urgency   string
	Priority  string
	Service   string
	ServiceID string
	Number    int
	ID        string
	URL       string
}

// wire mirrors the parts of a PagerDuty V3 webhook we read. The interesting object
// is under event.data; incident events carry status/urgency/priority/service.
type wire struct {
	Event struct {
		EventType    string `json:"event_type"`
		ResourceType string `json:"resource_type"`
		Data         struct {
			ID       string `json:"id"`
			Number   int    `json:"number"`
			Status   string `json:"status"`
			Title    string `json:"title"`
			HTMLURL  string `json:"html_url"`
			Urgency  string `json:"urgency"`
			Priority *struct {
				Summary string `json:"summary"`
			} `json:"priority"`
			Service *struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"service"`
		} `json:"data"`
	} `json:"event"`
}

// parse extracts normalized facts from a PagerDuty V3 webhook body.
func parse(body []byte) facts {
	var w wire
	_ = json.Unmarshal(body, &w)
	d := w.Event.Data
	f := facts{
		EventType: w.Event.EventType,
		Status:    d.Status,
		Title:     d.Title,
		Urgency:   d.Urgency,
		Number:    d.Number,
		ID:        d.ID,
		URL:       d.HTMLURL,
	}
	if d.Priority != nil {
		f.Priority = d.Priority.Summary
	}
	if d.Service != nil {
		f.Service, f.ServiceID = d.Service.Summary, d.Service.ID
	}
	return f
}
