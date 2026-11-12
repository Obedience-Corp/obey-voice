package brain

// hostedProviders classifies every registered brain provider by whether it
// sends conversation text off this machine. A provider missing from it is
// treated as hosted.
var hostedProviders = map[string]bool{
	"ollama":     false,
	"claude":     true,
	"codex":      true,
	"cursor":     true,
	"grok":       true,
	"muse-spark": true, // Muse's meta provider is Meta's cloud service
	"obey":       true, // the daemon routes to a provider Voice cannot inspect
}

// IsHosted reports whether a persona on the named provider sends conversation
// text to a service outside this machine. An empty name is the default
// provider, Ollama. Unknown providers are hosted.
func IsHosted(provider string) bool {
	name := normalizeProvider(provider)
	if name == "" {
		name = "ollama"
	}
	hosted, known := hostedProviders[name]
	return hosted || !known
}

// HostedSharedHistory reports whether a provider keeps the shared history and
// sends it to a hosted service. Such a provider sends only its working view,
// which the host limits to its persona's share of the conversation; the rest
// of the history it keeps stays on this machine.
func HostedSharedHistory(p Provider) bool {
	_, ok := p.(interface {
		ContinuityTransferProvider
		sendsWorkingView()
	})
	return ok
}

func (*CodexBrain) sendsWorkingView()     {}
func (*MuseSparkBrain) sendsWorkingView() {}
func (*ObeyBrain) sendsWorkingView()      {}
