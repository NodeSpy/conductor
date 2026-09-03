package config

import (
	"strings"
	"testing"
)

// A ${VAR} that appears only inside a YAML comment must not make the config fail
// to load — the example config's own comments mention ${ENV}/${GH_PAT}, and a
// daemon that hard-errored on those crash-looped on startup (regression).
func TestExpandEnvIgnoresCommentVars(t *testing.T) {
	t.Setenv("DEFINED", "xyz")
	in := "# see ${ENV} and ${GH_PAT} in the docs\n" +
		"secret: ${DEFINED}   # trailing ${ALSO_UNDEFINED} note\n" +
		"cmd: \"{{.repo}}#{{.pr}}\"\n"
	out, err := expandEnv("/tmp/config.yaml", []byte(in))
	if err != nil {
		t.Fatalf("undefined vars only in comments must not error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "secret: xyz") {
		t.Errorf("defined var in a value should expand: %q", s)
	}
	for _, want := range []string{"${ENV}", "${GH_PAT}", "${ALSO_UNDEFINED}"} {
		if !strings.Contains(s, want) {
			t.Errorf("comment %s should be left verbatim: %q", want, s)
		}
	}
	if !strings.Contains(s, "{{.repo}}#{{.pr}}") {
		t.Errorf("a mid-token # (not whitespace-preceded) must stay code: %q", s)
	}
}

// A real undefined var in a value (not a comment) still errors, naming it.
func TestExpandEnvErrorsOnUndefinedValue(t *testing.T) {
	_, err := expandEnv("/tmp/config.yaml", []byte("secret: ${REALLY_NOT_SET_XYZ}\n"))
	if err == nil {
		t.Fatal("an undefined var in a value should error")
	}
	if !strings.Contains(err.Error(), "REALLY_NOT_SET_XYZ") {
		t.Errorf("error should name the missing var: %v", err)
	}
}
