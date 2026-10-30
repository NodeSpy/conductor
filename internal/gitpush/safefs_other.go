//go:build !unix

package gitpush

import "errors"

var errUnsupported = errors.New("gitpush: not supported on this platform")

func readUnder(string, string, int64) ([]byte, error) { return nil, errUnsupported }
func writeUnder(string, string, []byte, uint32) error { return errUnsupported }
func mkdirUnder(string, string, string) error         { return errUnsupported }
func snapshotObjects(string, string) error            { return errUnsupported }
