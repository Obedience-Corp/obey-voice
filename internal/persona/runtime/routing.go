package personaruntime

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// Route is the shared finalized-input operation for text and spoken requests.
// Partial transcripts never call this hook. A missing address keeps the
// selected responder; the audio front end owns echo prevention independently.
func (s *Selector) Route(ctx context.Context, input string, background bool) (pipeline.ResponseAdmission, error) {
	admitted := pipeline.ResponseAdmission{Request: input}
	bare := false
	if !background {
		profiles, err := s.opts.Catalog.List()
		if err != nil {
			return admitted, err
		}
		identities := make([]summon.Identity, 0, len(profiles))
		for _, p := range profiles {
			aliases := append([]string(nil), p.Aliases...)
			if p.ID == s.opts.Initial.PersonaID {
				aliases = append(aliases, s.opts.Base.NameSummonAliases...)
			}
			identities = append(identities, summon.Identity{ID: p.ID, DisplayName: p.DisplayName, Aliases: aliases})
		}
		roster, err := summon.NewRoster(identities)
		if err != nil {
			return admitted, err
		}
		address, err := roster.Resolve(input)
		if err != nil {
			return admitted, err
		}
		if address.PersonaID != "" {
			if _, err := s.Select(ctx, address.PersonaID, address.Reason); err != nil {
				return admitted, err
			}
			admitted.Request, bare = address.Remainder, address.Bare
		}
	}
	envelope, err := s.Admit(ctx)
	if err != nil {
		return admitted, err
	}
	admitted.Envelope = envelope
	if bare {
		admitted.LocalReply = envelope.DisplayName + " here."
	}
	return admitted, nil
}
