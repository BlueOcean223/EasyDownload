package icons

import (
	_ "embed"
)

// FaviconICO is the application icon in ICO format (for Windows)
//
//go:embed favicon.ico
var FaviconICO []byte

// FaviconPNG is the application icon in PNG format (cross-platform)
//
//go:embed favicon.png
var FaviconPNG []byte

// DefaultIcon is the default tray icon (using ICO for Windows compatibility)
var DefaultIcon = FaviconICO

// ActiveIcon is the same as DefaultIcon
var ActiveIcon = FaviconICO
