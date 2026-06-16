package direct

// The max simultaneous sources/outputs is no longer a constant — it is set
// per-process via PANAUDIA_SPACE_MAX_SOURCES and plumbed through
// NewDirectBackend / NewDefaultDirectSpace as maxSources.
const DIRECT_ORDER = 3
