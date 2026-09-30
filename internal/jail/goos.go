package jail

import "runtime"

// isDarwin: the macOS jail sees host paths as they are (no mount namespace).
var isDarwin = runtime.GOOS == "darwin"
