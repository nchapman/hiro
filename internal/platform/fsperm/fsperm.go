// Package fsperm defines canonical filesystem permission constants used
// across the platform. Centralising these avoids duplicate definitions
// with inconsistent names in every package that creates files or directories.
package fsperm

import "os"

const (
	// DirPrivate restricts access to owner only (rwx------).
	// Used for config dirs, secrets, instance dirs, socket dirs.
	DirPrivate = os.FileMode(0o700)

	// FilePrivate restricts access to owner only (rw-------).
	// Used for config files, secrets, database files, identity keys.
	FilePrivate = os.FileMode(0o600)

	// DirStandard is the default directory permission (rwxr-xr-x).
	// Owner has full access; group and others can read and traverse.
	DirStandard = os.FileMode(0o755)

	// FileStandard is the default file permission (rw-r--r--).
	// Owner can read/write; group and others can read.
	FileStandard = os.FileMode(0o644)

	// DirSetgid is a setgid group-writable directory (rwxrwsr-x).
	// New files inherit the directory's group. Used for collaborative
	// directories where multiple agent UIDs need write access.
	DirSetgid = os.FileMode(0o2775)

	// DirShared is a group-writable directory without setgid (rwxrwxr-x).
	// Owner and group have full access; others can read and traverse.
	DirShared = os.FileMode(0o775)

	// FileCollaborative is a group-writable file (rw-rw-r--).
	// Owner and group can read/write; others can read.
	FileCollaborative = os.FileMode(0o664)
)
