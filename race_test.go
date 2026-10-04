//go:build race

package geta_test

// raceEnabled reports whether the race detector is on, under which sync.Pool
// drops some of what is put in it and allocation counts do not hold.
const raceEnabled = true
