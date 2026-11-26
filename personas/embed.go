// Package personas embeds the persona packs that ship in the binary
// (ADR-0012: original personas only; ADR-0036: pack format). It holds no
// audio and no voice weights (ADR-0020); each pack's [voice] names a stock
// interim voice from the models catalog.
package personas

import "embed"

// FS holds one folder per shipped persona.
//
//go:embed ashford klaus pettigrew lucy
var FS embed.FS
