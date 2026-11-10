package plugin

import (
	"context"
	"fmt"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// widgetDecl is a synthetic connector declaration with the given
// capabilities.
func widgetDecl(c Capabilities) func(context.Context, Spec) (*Decl, error) {
	return func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "widget", Capabilities: c,
			Connection: sdk.Schema{"listen": {Type: "string"}, "key_path": {Type: "string"}}}, nil
	}
}

var (
	widgetNarrow = Capabilities{Egress: []string{"api.widget.example:443"}}
	widgetWide   = Capabilities{
		Egress: []string{"api.widget.example:443"},
		Listen: []string{"config:listen"},
		Read:   []string{"config:key_path"},
	}
)

func logInto(lines *[]string) func(string, ...any) {
	return func(format string, args ...any) { *lines = append(*lines, fmt.Sprintf(format, args...)) }
}
