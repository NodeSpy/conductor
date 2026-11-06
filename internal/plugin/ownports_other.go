//go:build !linux && !darwin

package plugin

// conductorListenPorts: no plugin jail here (buildJailed refuses).
func conductorListenPorts() (map[int]bool, error) { return map[int]bool{}, nil }
