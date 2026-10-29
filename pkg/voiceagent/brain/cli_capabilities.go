package brain

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	claudeSDKVersion = "v1.4.0"
	grokSDKVersion   = "v0.2.1"
)

func unsupported(evidence string) config.Capability {
	return config.Capability{Evidence: evidence}
}

func supported(layer, evidence string) config.Capability {
	return config.Capability{Supported: true, Layer: layer, Evidence: evidence}
}

// CLIWorkerCapabilities returns source-verified capabilities for the CLI
// adapters and managed delegation. It describes observable enforcement only;
// it does not treat provider billing, nested CLI tools, or descendants as
// visible merely because a parent process exists.
func CLIWorkerCapabilities(provider string) config.WorkerCapabilities {
	p := strings.ToLower(strings.TrimSpace(provider))
	base := config.WorkerCapabilities{Provider: p}
	base.OutputTokenCeiling = unsupported("CLI adapters do not expose an adapter-owned output/reasoning token ceiling")
	base.TurnBoundary = unsupported("CLI internal tool turns and nested requests are not exposed as an adapter-owned boundary")
	base.ToolCallMediation = unsupported("CLI owns tool execution; adapter sees no complete nested tool-call stream")
	base.AuthoritativeBilling = unsupported("CLI result cost is provider-reported and account/billing authority is outside adapter")
	base.ChargeBound = unsupported("no atomic reservation or verified worst-case bound for retries, nested tools, and cancellation")
	base.DescendantExit = unsupported("context cancellation observes the CLI process only; descendants are not observed")
	base.ResumablePreemption = unsupported("session resume exists, but preemption and replay do not establish billing or descendant termination")

	switch p {
	case "claude":
		base.Adapter, base.Version = "claude-code-go", claudeSDKVersion
		base.RequestBoundary = unsupported("adapter operation is not a provider request count; CLI nested requests remain opaque")
		base.TurnBoundary = unsupported("Claude MaxTurns is deprecated and ignored by the current CLI")
		base.RetryLimit = unsupported("brain retries only stale resume once; current call and CLI internal retries are not bounded")
		base.RuntimeLimit = supported("context/CLI", "RunPromptCtx and StreamPrompt accept context; process cancellation is observable")
		base.Usage = supported("brain OnWorkerUsage", "streaming assistant usage and full result cost are reported; unavailable fields remain unknown")
		base.ProcessExit = supported("context/CLI", "helper CLI process wait is observable; remote/descendant termination is not implied")
		base.CancellationRequested = supported("context/CLI", "context cancellation requests termination of the CLI process")
	case "grok":
		base.Adapter, base.Version = "grok-go-sdk", grokSDKVersion
		base.RequestBoundary = unsupported("adapter operation is not a provider request count; CLI nested requests remain opaque")
		base.TurnBoundary = unsupported("SDK can emit --max-turns, but this adapter leaves it unset")
		base.RetryLimit = unsupported("brain retries only stale resume once; SDK retry policy is not configured and CLI internal retries are unknown")
		base.RuntimeLimit = supported("context/CLI", "RunPromptCtx and StreamPrompt accept context; process cancellation is observable")
		base.Usage = supported("brain OnWorkerUsage", "full result cost is reported; Grok streaming events expose no token/cost fields, which remain unknown")
		base.ProcessExit = supported("context/CLI", "helper CLI process wait is observable; remote/descendant termination is not implied")
		base.CancellationRequested = supported("context/CLI", "context cancellation requests termination of the CLI process")
	case "cursor":
		base.Adapter, base.Version = "cursor-agent-go", "v0.0.0-20260903232116-1c77e507477f"
		base.RequestBoundary = unsupported("one print-mode turn can contain opaque model and tool requests")
		base.RetryLimit = unsupported("one stale-session retry; CLI internal retries are not bounded by this adapter")
		base.RuntimeLimit = supported("context/CLI", "AskCtx passes context to exec.CommandContext and waits for the direct process")
		base.Usage = unsupported("print-mode counters feed conversation usage, not the worker accounting callback")
		base.ProcessExit = supported("exec.Run", "direct CLI exit is observed; descendant and remote termination are not implied")
		base.CancellationRequested = supported("context/CLI", "context cancellation requests termination of the direct CLI process")
	case "muse-spark":
		base.Adapter, base.Version = "muse-spark-go", "v0.1.0"
		base.RequestBoundary = unsupported("one exec run may include opaque model and native tool requests")
		base.RetryLimit = unsupported("adapter does not retry; native internal retries are outside its control")
		base.RuntimeLimit = supported("context/CLI", "RunPromptCtx and StreamPrompt cancel and wait for the owned process")
		base.Usage = unsupported("adapter reports unknown worker usage; native usage is not aggregated")
		base.ProcessExit = supported("exec.Wait", "owned CLI process exit is observed; descendants and remote work are not")
		base.CancellationRequested = supported("context/CLI", "context cancellation terminates the owned CLI process")
		base.ResumablePreemption = unsupported("adapter replays host history; native session resume is not used")
	case "codex":
		base.Adapter, base.Version = "codex app-server", "v2 (tested 0.163.0)"
		base.RequestBoundary = unsupported("one app-server turn can contain multiple model requests")
		base.TurnBoundary = unsupported("one app-server turn can contain opaque model/tool iterations; no iteration ceiling")
		base.RetryLimit = unsupported("no adapter retries; app-server internal retries remain opaque")
		base.RuntimeLimit = supported("context/app-server", "cancellation interrupts the turn then kills and reaps the local process group")
		base.Usage = supported("thread/tokenUsage/updated", "fresh thread cumulative tokens; resumed thread and monetary usage remain unknown")
		base.ProcessExit = supported("exec.Wait", "local app-server exit is observed; remote work termination is not implied")
		base.CancellationRequested = supported("turn/interrupt", "bounded interrupt RPC followed by local process cleanup")
	case "managed", "delegation":
		base.Adapter, base.Version = "obey delegation CLI", "schema v1"
		base.RequestBoundary = supported("delegation runtime", "one admitted job; same idempotency key across at most three Start attempts")
		base.TurnBoundary = unsupported("delegation snapshots expose worker state, not model turn boundaries")
		base.RetryLimit = supported("delegation runtime", "Start retries are hard bounded to three attempts")
		base.RuntimeLimit = supported("delegation runtime", "job deadline plus bounded Start/Await/Cancel RPC contexts")
		base.Usage = unsupported("delegation snapshots expose no model token usage")
		base.ProcessExit = supported("delegation CLI", "helper CLI process wait is observable; remote/descendant termination is not implied")
		base.CancellationRequested = supported("delegation runtime", "Cancel is sent explicitly; nonterminal acceptance remains unknown")
	default:
		base.Adapter, base.Version = "unknown", "unknown"
		base.RequestBoundary = unsupported("provider adapter is unknown")
		base.TurnBoundary = unsupported("provider adapter is unknown")
		base.RetryLimit = unsupported("provider adapter is unknown")
		base.RuntimeLimit = unsupported("provider adapter is unknown")
		base.Usage = unsupported("provider adapter is unknown")
		base.ProcessExit = unsupported("provider adapter is unknown")
		base.CancellationRequested = unsupported("provider adapter is unknown")
	}
	return base
}
