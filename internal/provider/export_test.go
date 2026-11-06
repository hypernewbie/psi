package provider

// AuthReads returns the number of shared auth.json reads since the last
// ResetAuthReads call.
func AuthReads() int64 { return authReads.Load() }

// ResetAuthReads clears the shared auth.json read counter.
func ResetAuthReads() { authReads.Store(0) }
