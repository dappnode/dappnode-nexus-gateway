package domain

import "time"

// ProviderModel represents an upstream provider's model configuration.
type ProviderModel struct {
	ID                string
	ProviderName      string
	ProviderModelName string
	Active            bool
	CreatedAt         time.Time
}
