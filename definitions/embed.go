// Package definitions carries the capability definitions as data: for every
// capability a directory with its input schema, the keys the renderer drops
// and the fleet policy. The render library reads them from FS; nothing else
// is compiled in.
package definitions

import "embed"

// FS holds every file under definitions/<capability>/.
//
//go:embed agent-platform/*.json agent-platform/*.yaml customer-portal/*.json customer-portal/*.yaml cluster-mcp-servers/*.json cluster-mcp-servers/*.yaml
var FS embed.FS
