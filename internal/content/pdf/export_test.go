package pdf

// Extract exposes the synchronous extraction to the external test package, so
// a test can observe when the extraction itself stops (Convert returns on
// cancellation without waiting for it).
var Extract = extract
