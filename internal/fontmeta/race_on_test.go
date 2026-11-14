//go:build race

package fontmeta

// raceSlowdown: the race detector makes code several times slower.
const raceSlowdown = 20
