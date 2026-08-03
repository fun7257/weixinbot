package media

// DefaultMaxBytes is the shared default cap for inbound MediaStore.Save and CDN
// download/upload response bodies (100 MiB). Keep CDN MaxDownloadBytes and
// Store.MaxBytes aligned with this constant unless a caller overrides both.
const DefaultMaxBytes = 100 * 1024 * 1024

// DefaultDownloadMaxBytes is an alias of DefaultMaxBytes for CDN clarity.
const DefaultDownloadMaxBytes = DefaultMaxBytes
