// Package record implements BeachDB's shared append-log record framing.
//
// The record package owns only the physical envelope around an opaque payload:
// magic, version, record type, payload length, and checksum. It does not know
// whether the payload is a WAL batch, a manifest VersionEdit, or some future
// metadata record.
//
// WAL and manifest files use this package with different magic values so tools
// and recovery code can reject the wrong file type early while sharing the same
// checksum, length, and truncation handling.
package record
