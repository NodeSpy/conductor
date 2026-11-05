package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/scratch"
)

// CallStagingPrefix names the per-call staging directories made inside an
// instance's staging directory.
const CallStagingPrefix = "call-"

type callStagingKey struct{}

// WithCallStaging hands a plugin call the staging directory to write its
// files to (plugin-contract.md Q7). The caller owns it: it removes it once
// the call's files are stored, whether the call succeeded or not.
func WithCallStaging(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, callStagingKey{}, dir)
}

// CallStaging is the staging directory WithCallStaging set, or "".
func CallStaging(ctx context.Context) string {
	s, _ := ctx.Value(callStagingKey{}).(string)
	return s
}

// NewCallStaging makes a fresh call staging directory inside an instance's
// staging directory.
func NewCallStaging(instanceDir string) (string, error) {
	return os.MkdirTemp(instanceDir, CallStagingPrefix+"*")
}

// PluginTmpPrefix names a plugin process's TMPDIR in its staging directory
// (<staging root>/<plugin>/.tmp-*): never an instance name.
const PluginTmpPrefix = scratch.PluginTmpPrefix

// SweepCallStaging removes call staging directories older than olderThan
// under the plugin staging root (<root>/<plugin>/<instance>/call-*), and the
// TMPDIRs of plugin processes that are gone: what a daemon that died
// mid-call left behind.
func SweepCallStaging(root string, olderThan time.Duration) int {
	n := 0
	plugins, _ := os.ReadDir(root)
	for _, p := range plugins {
		pdir := filepath.Join(root, p.Name())
		// A plugin process's TMPDIR, removed when it exits; one whose
		// process died is unheld.
		n += scratch.SweepUnheld(pdir, PluginTmpPrefix, time.Minute)
		instances, _ := os.ReadDir(pdir)
		for _, in := range instances {
			if in.IsDir() && !strings.HasPrefix(in.Name(), PluginTmpPrefix) {
				n += scratch.Sweep(root+"/"+p.Name()+"/"+in.Name(), CallStagingPrefix, olderThan)
			}
		}
	}
	return n
}
