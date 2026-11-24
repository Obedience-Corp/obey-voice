package brain

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
)

const jobControlToolName = "job_control"

// JobControlRoute is StreamResult.Route for a turn that ended at a job_control
// call. StreamResult.JobControl carries the call or its refusal.
const JobControlRoute = "job_control"

const jobControlDescription = `Control one of YOUR OWN background jobs from the list of jobs you are given. Use it only when the user asks you to end a job, asks what a job has so far, or corrects what a job should do. action "cancel" ends the job. action "report" makes the job stop and say what it has done; "that's enough" or "what have you got so far" means report. action "steer" gives the running job a correction; put the correction in "instruction". job_id must be copied exactly from the list. Never control a job another persona started. A bare "stop" is not a request to cancel a job. If it is not clear which job the user means, ask instead of calling this tool. Do not use start_background_work to change a job that is already running. Call this tool alone and say nothing else; the host tells the user what happened.`

func jobControlTool() api.Tool {
	return api.Tool{Type: "function", Function: api.ToolFunction{
		Name:        jobControlToolName,
		Description: jobControlDescription,
		Parameters: api.ToolFunctionParameters{
			Type:     "object",
			Required: []string{"action", "job_id"},
			Properties: orderedProps(map[string]api.ToolProperty{
				"action":      {Type: api.PropertyType{"string"}, Enum: []any{"cancel", "report", "steer"}},
				"job_id":      {Type: api.PropertyType{"string"}},
				"instruction": {Type: api.PropertyType{"string"}, Description: "The correction, for steer."},
			}),
		},
	}}
}

// JobControlAction is what a job_control call asks of a job.
type JobControlAction string

const (
	JobControlCancel JobControlAction = "cancel"
	JobControlReport JobControlAction = "report"
	JobControlSteer  JobControlAction = "steer"
)

// JobControlRequest is one decoded job_control call. JobID is exactly what
// the model wrote.
type JobControlRequest struct {
	Action      JobControlAction
	JobID       string
	Instruction string
}

// JobControlRefusal is why a job_control batch was not decoded into a request.
type JobControlRefusal string

const (
	// JobControlNotOffered: the request that returned the call did not offer
	// the tool.
	JobControlNotOffered JobControlRefusal = "not_offered"
	// JobControlIncomplete: the response failed, stopped early, or was cut at
	// its output limit.
	JobControlIncomplete JobControlRefusal = "incomplete"
	// JobControlMalformed: a field is missing, of the wrong type or unknown,
	// or a steer has no instruction.
	JobControlMalformed JobControlRefusal = "malformed"
	// JobControlNoJob: the call names no job.
	JobControlNoJob JobControlRefusal = "no_job"
	// JobControlAmbiguous: the batch holds two different job_control calls.
	JobControlAmbiguous JobControlRefusal = "ambiguous"
)

// JobControl is a live turn that ended at a job_control call: the request,
// or why there is none. Nothing in the batch ran. Spoken is the text the
// model's last round sent before the call.
type JobControl struct {
	Request *JobControlRequest
	Refusal JobControlRefusal
	Spoken  string
}

// offersJobControl reports whether a turn's tools include job_control: the
// host can authorize a control on it, the configuration turns the tool on,
// and it is a live direct turn answering a person.
func (o *OllamaBrain) offersJobControl(opts StreamOptions) bool {
	return opts.JobControl && o.catchesJobControl() && opts.ToolsEnabled && o.offersViewImage(opts) &&
		opts.Control == nil && !opts.OmitTurnInstruction && !opts.RetryInput
}

// catchesJobControl reports whether a job_control call ends the turn instead
// of running as a tool, which is the case wherever the configuration turns
// the tool on, offered or not.
func (o *OllamaBrain) catchesJobControl() bool {
	return o.cfg != nil && o.cfg.VoiceJobControl
}

// decodeJobControl reads a tool batch holding a job_control call. offered and
// complete describe the request and response that returned it.
func decodeJobControl(calls []api.ToolCall, offered, complete bool, spoken string) JobControl {
	refuse := func(r JobControlRefusal) JobControl { return JobControl{Refusal: r, Spoken: spoken} }
	if !offered {
		return refuse(JobControlNotOffered)
	}
	if !complete {
		return refuse(JobControlIncomplete)
	}
	var controls []map[string]any
	seen := map[string]bool{}
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Name) != jobControlToolName {
			continue
		}
		args := call.Function.Arguments.ToMap()
		key, err := json.Marshal(args)
		if err != nil {
			return refuse(JobControlMalformed)
		}
		if !seen[string(key)] {
			seen[string(key)] = true
			controls = append(controls, args)
		}
	}
	if len(controls) != 1 {
		return refuse(JobControlAmbiguous)
	}
	req, refusal := decodeJobControlArgs(controls[0])
	if refusal != "" {
		return refuse(refusal)
	}
	return JobControl{Request: req, Spoken: spoken}
}

func decodeJobControlArgs(args map[string]any) (*JobControlRequest, JobControlRefusal) {
	for key := range args {
		if key != "action" && key != "job_id" && key != "instruction" {
			return nil, JobControlMalformed
		}
	}
	action, ok := args["action"].(string)
	if !ok {
		return nil, JobControlMalformed
	}
	req := &JobControlRequest{Action: JobControlAction(action)}
	switch req.Action {
	case JobControlCancel, JobControlReport, JobControlSteer:
	default:
		return nil, JobControlMalformed
	}
	if raw, ok := args["instruction"]; ok {
		if req.Instruction, ok = raw.(string); !ok {
			return nil, JobControlMalformed
		}
	}
	raw, ok := args["job_id"]
	if !ok {
		return nil, JobControlNoJob
	}
	if req.JobID, ok = raw.(string); !ok {
		return nil, JobControlMalformed
	}
	if strings.TrimSpace(req.JobID) == "" {
		return nil, JobControlNoJob
	}
	if req.Action == JobControlSteer && strings.TrimSpace(req.Instruction) == "" {
		return nil, JobControlMalformed
	}
	if req.Action != JobControlSteer {
		req.Instruction = ""
	}
	return req, ""
}

type offeredKey struct{}

// withOfferProbe has each request attempt sent under ctx record in offered
// whether it offered job_control, so a response is judged by the attempt
// that produced it, a retry included.
func withOfferProbe(ctx context.Context, offered *bool) context.Context {
	return context.WithValue(ctx, offeredKey{}, offered)
}

func noteOffered(ctx context.Context, tools api.Tools) {
	if offered, ok := ctx.Value(offeredKey{}).(*bool); ok {
		*offered = offersTool(tools, jobControlToolName)
	}
}

// withoutJobControl is tools for a repair or retry attempt, which never
// offers job_control.
func withoutJobControl(tools api.Tools) api.Tools {
	if !offersTool(tools, jobControlToolName) {
		return tools
	}
	out := make(api.Tools, 0, len(tools)-1)
	for _, tool := range tools {
		if tool.Function.Name != jobControlToolName {
			out = append(out, tool)
		}
	}
	return out
}

func offersTool(tools api.Tools, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// spokenBeforeControl is what the round ending at a job_control call sent.
func (o *OllamaBrain) spokenBeforeControl(output *ollama.Round) string {
	return strings.TrimSpace(stripReplyConventions(o.cfg.AgentName, output.EmittedText()))
}
