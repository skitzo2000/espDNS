// Package web holds the dashboard's pages, built into the binary, and the third-party code
// they load: vendor/esp-web-tools/ (the builder's flasher, a pinned copy served from here,
// updated with `make vendor-ewt`; its THIRD_PARTY.md) and vendor/esptool-js/ (what
// flash.js runs, esptool-js's ES module bundle at the version esp-web-tools was built with,
// `make vendor-etj`).
package web

import "embed"

//go:embed *.html *.css *.js img/* vendor/esp-web-tools/* vendor/esptool-js/*
var Files embed.FS
