package skills

import _ "embed"

//go:embed gh-autoaccount/SKILL.md
var content string

// Content returns the bundled skill exactly as shipped.
func Content() string { return content }

// Install installs the bundled skill under dir without replacing existing content.
func Install(dir string) (string, bool, error) { return install(dir, Content()) }
