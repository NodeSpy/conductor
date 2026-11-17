//go:build !linux && !darwin

package repocache

// prepareTarget, where caches are not restored by fd: the target must be
// absent or an empty directory.
func prepareTarget(base, target string, replace bool) error {
	exists, err := safeTarget(base, target)
	if err != nil {
		return err
	}
	if !exists {
		return mkdirUnder(base, target)
	}
	return nil
}
