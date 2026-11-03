// Package web holds the dashboard's pages, built into the binary, and the third-party code
// they load: vendor/esptool-js/ (what flash.js runs for Install over USB and Write keys
// only, esptool-js's ES module bundle, a pinned copy served from here, updated with
// `make vendor-etj`; its THIRD_PARTY.md).
package web

import "embed"

//go:embed *.html *.css *.js img/* vendor/esptool-js/*
var Files embed.FS
