package adapters

import "context"

type ProbeResult struct {
	Service string `json:"service"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

type Probe interface {
	Probe(ctx context.Context) ProbeResult
}
