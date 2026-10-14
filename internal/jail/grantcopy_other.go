//go:build !unix

package jail

import "errors"

func copyNoFollow(root, rel, dst string) (string, error) {
	return "", errors.New("grant file pinning is not supported on this platform")
}

func ownPrivateDir(dir string) error { return errors.New("not supported on this platform") }
