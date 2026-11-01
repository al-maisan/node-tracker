// Package deploy embeds the systemd units, so `node-tracker install-service`
// writes the units that the release archives and packages ship.
package deploy

import "embed"

// Systemd holds systemd/system/* and systemd/user/*.
//
//go:embed systemd
var Systemd embed.FS
