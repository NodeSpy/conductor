package connector

import "sync"

// testTypeOnces guards fixture connector types this package's tests register
// at TEST time (not init time, unlike a real builtin). `go test -count=N`
// reruns every test function N times within the SAME process, so a fixture
// registered unconditionally on every call panics on its second iteration
// ("type registered twice") even though it is the identical declaration
// every time. registerTypeForTest makes that idempotent, scoped to one name,
// without touching RegisterType's own panic-on-dup — a GENUINE collision (two
// DIFFERENT fixtures racing for the same type name) still panics the first
// time either of them calls through here for real.
var testTypeOnces sync.Map // name (string) -> *sync.Once

// registerTypeForTest registers decl/b through RegisterType exactly once per
// decl.Type, even across go test -count=N reruns in one process.
func registerTypeForTest(decl *TypeDecl, b Builder) {
	v, _ := testTypeOnces.LoadOrStore(decl.Type, &sync.Once{})
	v.(*sync.Once).Do(func() { RegisterType(decl, b) })
}
