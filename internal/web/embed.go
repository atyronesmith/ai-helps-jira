package web

import "embed"

//go:embed static/css/*.css static/js/*.js
var StaticFS embed.FS

//go:embed templates/*.html
var TemplateFS embed.FS
