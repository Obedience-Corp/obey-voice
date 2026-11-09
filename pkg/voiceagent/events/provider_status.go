package events

import (
	"crypto/sha256"
	"fmt"
	"sync/atomic"
)

// ProviderStatus describes inference health, independently of audio devices.
// Binding is opaque: endpoint credentials and paths never enter UI events.
type ProviderStatus struct {
	Provider string `json:"provider"`
	Role     string `json:"role"`
	Model    string `json:"model"`
	Binding  string `json:"binding"`
	State    string `json:"state"`
	Message  string `json:"message"`
	Revision uint64 `json:"revision"`
}

var providerStatusRevision atomic.Uint64

func NewProviderStatus(provider, role, model, endpoint, state, message string) *ProviderStatus {
	key := sha256.Sum256([]byte(provider + "\x00" + role + "\x00" + model + "\x00" + endpoint))
	return &ProviderStatus{Provider: provider, Role: role, Model: model,
		Binding: fmt.Sprintf("%x", key[:16]), State: state, Message: message,
		Revision: providerStatusRevision.Add(1)}
}
