// Package schemas embeds the initial, unreleased state schema. Future upgrades
// require transactional migrations and a consistent SQLite backup first.
package schemas

import _ "embed"

const StateVersion = 1

//go:embed state-schema.sql
var StateSQL string
