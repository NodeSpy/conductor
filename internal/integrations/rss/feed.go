package rss

import (
	"encoding/xml"
	"strings"
)

// Item is the normalized subset of a feed entry we trigger on, common to RSS 2.0
// and Atom.
type Item struct {
	Title     string
	Link      string
	ID        string // GUID (RSS) or id (Atom); falls back to Link
	Summary   string
	Published string
}

// dedupID returns a stable id for the item (GUID/id, else link, else title).
func (i Item) dedupID() string {
	switch {
	case i.ID != "":
		return i.ID
	case i.Link != "":
		return i.Link
	default:
		return i.Title
	}
}

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			GUID        string `xml:"guid"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomDoc struct {
	XMLName xml.Name `xml:"feed"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		ID        string `xml:"id"`
		Summary   string `xml:"summary"`
		Content   string `xml:"content"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
	} `xml:"entry"`
}

// parseFeed decodes an RSS 2.0 or Atom document into normalized items. It tries
// RSS first, then Atom, so a mixed diet of changelog feeds all work.
func parseFeed(body []byte) []Item {
	var r rssDoc
	if err := xml.Unmarshal(body, &r); err == nil && len(r.Channel.Items) > 0 {
		out := make([]Item, 0, len(r.Channel.Items))
		for _, it := range r.Channel.Items {
			out = append(out, Item{
				Title:     strings.TrimSpace(it.Title),
				Link:      strings.TrimSpace(it.Link),
				ID:        strings.TrimSpace(firstNonEmpty(it.GUID, it.Link)),
				Summary:   strings.TrimSpace(it.Description),
				Published: strings.TrimSpace(it.PubDate),
			})
		}
		return out
	}

	var a atomDoc
	if err := xml.Unmarshal(body, &a); err == nil && len(a.Entries) > 0 {
		out := make([]Item, 0, len(a.Entries))
		for _, e := range a.Entries {
			link := atomLink(e.Links)
			out = append(out, Item{
				Title:     strings.TrimSpace(e.Title),
				Link:      link,
				ID:        strings.TrimSpace(firstNonEmpty(e.ID, link)),
				Summary:   strings.TrimSpace(firstNonEmpty(e.Summary, e.Content)),
				Published: strings.TrimSpace(firstNonEmpty(e.Published, e.Updated)),
			})
		}
		return out
	}
	return nil
}

// atomLink prefers rel="alternate" (the human page), else the first link.
func atomLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}) string {
	for _, l := range links {
		if l.Rel == "alternate" || l.Rel == "" {
			return strings.TrimSpace(l.Href)
		}
	}
	if len(links) > 0 {
		return strings.TrimSpace(links[0].Href)
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
