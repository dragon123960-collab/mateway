package assets

import "embed"

// InitFS contains the default files used by `mateway init`.
//
//go:embed init/**
var InitFS embed.FS
