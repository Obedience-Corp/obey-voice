package discovery

import (
	"context"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// museCatalog reads the model ids Muse serves for the provider the brain runs.
// Tests replace it.
var museCatalog = func(ctx context.Context, client *muse.Client) ([]string, error) {
	list, err := client.ListModels(ctx, muse.HostOptions{Provider: "meta", NoSessionLog: true})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		ids = append(ids, m.ModelID)
	}
	return ids, nil
}

// discoverMuseSpark lists "default", then Muse's catalog, then the saved
// muse_spark_model if the catalog did not report it. A Muse that cannot answer
// leaves "default" and the saved model.
func discoverMuseSpark(ctx context.Context, cfg *config.Config) ProviderInfo {
	return museSparkProviderInfo(ctx, cfg, true)
}

// museSparkWithoutModels reports availability and the saved model without
// starting Muse.
func museSparkWithoutModels(cfg *config.Config) ProviderInfo {
	info := museSparkProviderInfo(context.Background(), cfg, false)
	info.ModelsPending = info.Available
	return info
}

func museSparkProviderInfo(parent context.Context, cfg *config.Config, list bool) ProviderInfo {
	info := ProviderInfo{Name: "muse-spark", Models: []string{"default"}}
	client, err := muse.NewClientFromPath()
	if err == nil {
		info.Available = true
		if list && parent.Err() == nil {
			ctx, cancel := context.WithTimeout(parent, probeTimeout)
			ids, err := museCatalog(ctx, client)
			cancel()
			if err == nil {
				for _, id := range ids {
					info.Models = appendModel(info.Models, id)
				}
			}
		}
	}
	if cfg != nil {
		info.Models = appendModel(info.Models, cfg.MuseSparkModel)
	}
	return info
}
