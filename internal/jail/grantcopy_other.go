//go:build !unix

package jail

import (
	"errors"
	"io"
)

func readNoFollow(root, rel string, w io.Writer) (string, error) {
	return "", errors.New("grant file pinning is not supported on this platform")
}

func ownPrivateDir(dir string) error { return errors.New("not supported on this platform") }

func layerHas(upper, rel string) bool { return false }
