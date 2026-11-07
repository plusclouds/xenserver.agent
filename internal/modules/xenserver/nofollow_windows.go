//go:build windows

package xenserver

// oNoFollow has no equivalent on Windows; the exclusive create (O_EXCL) of a
// random name still prevents writing through a pre-existing link.
const oNoFollow = 0
