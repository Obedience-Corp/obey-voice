// Separate module on purpose: it is what proves pkg/voiceagent is importable
// from outside the repository. An in-tree package would compile even if the
// library still leaned on internal/.
module example.com/samantha-embed

go 1.26.1

require github.com/Obedience-Corp/obey-voice v0.0.0

require (
	github.com/Obedience-Corp/cursor-agent-go v0.0.0-20260903232116-1c77e507477f // indirect
	github.com/Obedience-Corp/muse-spark-go v0.1.0 // indirect
	github.com/Obedience-Corp/obey-go-sdk v0.2.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gen2brain/malgo v0.11.25 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/k2-fsa/sherpa-onnx-go v1.13.8 // indirect
	github.com/k2-fsa/sherpa-onnx-go-linux v1.13.8 // indirect
	github.com/k2-fsa/sherpa-onnx-go-macos v1.13.8 // indirect
	github.com/k2-fsa/sherpa-onnx-go-windows v1.13.8 // indirect
	github.com/lancekrogers/claude-code-go v1.7.0 // indirect
	github.com/lancekrogers/grok-go-sdk v0.2.1 // indirect
	github.com/mailru/easyjson v0.9.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/ollama/ollama v0.32.3 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sagikazarmark/locafero v0.12.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/spf13/viper v1.21.0 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)

replace github.com/Obedience-Corp/obey-voice => ../..
