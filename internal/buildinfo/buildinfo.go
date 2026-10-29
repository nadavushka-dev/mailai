// Package buildinfo carries the version stamped at build time.
package buildinfo

// Version is set with -ldflags "-X .../buildinfo.Version=v1.2.3".
var Version = "dev"
