package main

import (
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core/coretest"
)

// The fixture forge connector stands in for a forge plugin under `use: github`.
func init() { connector.RegisterInProcessConnector(coretest.Forge) }

// reviewRequestedDelivery is a fixture delivery for the forge connector: a
// review requested on AcmeCorp/Widget#5300, already decoded to the event the
// plugin's translate returns (decoding the forge's payload is the plugin's
// job). One-shot and replay tests share it.
const reviewRequestedDelivery = `{"event": "review_requested", "body": {
  "event": "review_requested",
  "title": "auth: rework session refresh",
  "target": { "key": "AcmeCorp/Widget#5300", "url": "https://github.com/AcmeCorp/Widget/pull/5300",
    "assigned": true, "Repo": "AcmeCorp/Widget", "Owner": "AcmeCorp", "Name": "Widget",
    "PR": 5300, "Number": 5300, "HeadSHA": "cafebabe1234", "BaseRef": "main",
    "HTMLURL": "https://github.com/AcmeCorp/Widget/pull/5300" },
  "context": { "repo": "AcmeCorp/Widget", "owner": "AcmeCorp", "name": "Widget",
    "number": 5300, "pr": 5300, "head": "cafebabe1234", "base": "main",
    "head_branch": "feature/auth-refresh", "base_branch": "main",
    "title": "auth: rework session refresh", "author": "someone-else", "is_draft": false,
    "url": "https://github.com/AcmeCorp/Widget/pull/5300", "kind": "review_requested" }
}}`
