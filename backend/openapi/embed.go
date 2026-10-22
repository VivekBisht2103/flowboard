// Package openapi embeds the hand-maintained OpenAPI 3.1 contract served at /v1/openapi.yaml.
package openapi

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
