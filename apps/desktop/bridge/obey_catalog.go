package main

import (
	"context"
	"encoding/json"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func obeyCatalog(ctx context.Context, raw json.RawMessage) (any, error) {
	var request struct {
		Socket     string `json:"obey_socket"`
		Provider   string `json:"obey_provider"`
		Model      string `json:"obey_model"`
		ToolPolicy string `json:"obey_tool_policy"`
		Refresh    bool   `json:"refresh"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	cfg := &config.Config{BrainProvider: "obey", ObeySocket: request.Socket, ObeyProvider: request.Provider, ObeyModel: request.Model, ObeyToolPolicy: request.ToolPolicy}
	return discovery.DiscoverObeyRoute(ctx, cfg, request.Refresh), nil
}
