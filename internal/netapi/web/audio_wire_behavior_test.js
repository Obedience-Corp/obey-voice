/* Executable browser-consumer checks for the two bundled clients.
 * Run with: node internal/netapi/web/audio_wire_behavior_test.js
 */
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function element() {
  const listeners = new Map();
  const classes = new Set();
  const el = {
    value: "token",
    children: [],
    innerHTML: "",
    disabled: false,
    scrollTop: 0,
    scrollHeight: 0,
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      toggle: (name, force) => {
        const next = force === undefined ? !classes.has(name) : force;
        if (next) classes.add(name); else classes.delete(name);
        return next;
      },
    },
    addEventListener: (type, fn) => listeners.set(type, fn),
    click: () => listeners.get("click")?.({ target: null, preventDefault() {}, stopPropagation() {} }),
    appendChild(child) { this.children.push(child); },
    focus() {},
    _listeners: listeners,
  };
  let text = "";
  Object.defineProperty(el, "textContent", {
    get: () => text,
    set: (value) => { text = value; el.children.length = 0; },
  });
  return el;
}

function audioContext(sources) {
  return {
    state: "running",
    currentTime: 0,
    destination: {},
    resume: () => Promise.resolve(),
    createAnalyser: () => ({
      fftSize: 512,
      connect() {},
      getByteTimeDomainData(arr) { arr.fill(128); },
    }),
    createGain: () => ({ gain: { value: 1, exponentialRampToValueAtTime() {} }, connect() {} }),
    createMediaStreamSource: () => ({ connect() {}, disconnect() {} }),
    createScriptProcessor: () => ({ connect() {}, disconnect() {}, onaudioprocess: null }),
    createBuffer: (_channels, length, rate) => ({ duration: length / rate, copyToChannel() {} }),
    createBufferSource: () => {
      const src = {
        buffer: null,
        onended: null,
        connect() {},
        disconnect() {},
        start() { sources.push(src); },
        stop() { src.stopped = true; },
      };
      return src;
    },
    createOscillator: () => ({ connect() {}, start() {}, stop() {}, frequency: { value: 0 }, type: "sine" }),
    createBiquadFilter: () => ({ connect() {}, frequency: { value: 0 }, type: "lowpass" }),
  };
}

function makeEnvironment() {
  const ids = ["token", "pairCode", "pair", "start", "talk", "send", "interrupt", "clear", "input", "log", "status", "setup", "conversationTitle", "persona", "sessionsPanel", "sessions", "resume", "activity", "activitySummary", "gl", "fx", "pill", "hint", "sheet", "gear", "connect", "demo"];
  const elements = new Map(ids.map((id) => [id, element()]));
  const sources = [];
  const sockets = [];
  class FakeWebSocket {
    static OPEN = 1;
    constructor() {
      this.readyState = FakeWebSocket.OPEN;
      this.sent = [];
      sockets.push(this);
    }
    send(data) { this.sent.push(data); }
    close() { this.readyState = 3; }
  }
  const gl = new Proxy({}, { get: (_target, prop) => {
    if (prop === "getShaderParameter" || prop === "getProgramParameter") return () => true;
    return typeof prop === "string" && prop === prop.toUpperCase()
      ? 1
      : (...args) => (prop === "createShader" || prop === "createProgram" || prop === "createBuffer" ? {} : undefined);
  } });
  const fx = new Proxy({}, { get: () => () => {} });
  const document = {
    hidden: false,
    fullscreenElement: null,
    documentElement: { requestFullscreen: () => Promise.resolve() },
    getElementById: (id) => elements.get(id),
    createElement: () => element(),
    addEventListener() {},
    exitFullscreen: () => Promise.resolve(),
  };
  elements.get("gl").getContext = () => gl;
  elements.get("fx").getContext = () => fx;
  const local = { getItem: () => "token", setItem() {} };
  const win = { addEventListener() {} };
  const sandbox = {
    console,
    document,
    window: win,
    location: { protocol: "http:", host: "127.0.0.1:1", search: "" },
    localStorage: local,
    navigator: { mediaDevices: { getUserMedia: () => Promise.resolve({}) } },
    WebSocket: FakeWebSocket,
    AudioContext: function AudioContext() { return audioContext(sources); },
    webkitAudioContext: null,
    TextDecoder,
    ArrayBuffer,
    Uint8Array,
    DataView,
    Float32Array,
    URLSearchParams,
    performance: { now: () => 0 },
    requestAnimationFrame() {},
    setTimeout: () => 1,
    clearTimeout() {},
    fetch: () => Promise.reject(new Error("unused")),
    atob: (value) => Buffer.from(value, "base64").toString("binary"),
    Math,
    Promise,
    JSON,
    String,
    Number,
    Boolean,
    Set,
    Map,
  };
  win.AudioContext = sandbox.AudioContext;
  win.webkitAudioContext = null;
  return { sandbox, elements, sockets, sources };
}

async function exerciseConversation() {
  const env = makeEnvironment();
  env.sandbox.fetch = async (url) => url === "/v1/work" ? { status: 404 } : ({ ok: true, json: async () => ({ personas: [
    { id: "samantha", display_name: "Samantha", active: true },
    { id: "jarvis", display_name: "Jarvis" },
    { id: "disabled", display_name: "Disabled", inactive: true },
  ] }) });
  let releaseMic;
  let trackStopped = false;
  let micCalls = 0;
  env.sandbox.navigator.mediaDevices.getUserMedia = () => {
    micCalls++;
    return new Promise((resolve) => { releaseMic = () => resolve({ getTracks: () => [{ stop() { trackStopped = true; } }] }); });
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "app.js"), "utf8"), env.sandbox, { filename: "app.js" });
  env.elements.get("start").click();
  await new Promise((resolve) => setImmediate(resolve));
  const socket = env.sockets.at(-1);
  assert.ok(socket);
  socket.onopen();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(micCalls, 0, "connecting for text should not request the microphone");
  const message = (value) => socket.onmessage({ data: JSON.stringify(value) });
  const log = env.elements.get("log");
  message({ type: "session_snapshot", state_revision: 5, selection: { display_name: "Samantha", revision: 3 }, transcript: [
    { role: "user", content: "Hello" },
    { role: "assistant", content: "Earlier reply", persona_display_name: "Jarvis", response_id: "old" },
  ] });
  assert.deepEqual(log.children.map((node) => node.textContent), ["You: Hello", "Jarvis: Earlier reply"]);
  assert.equal(env.elements.get("conversationTitle").textContent, "Conversation · Samantha");
  const persona = env.elements.get("persona");
  assert.equal(persona.value, "samantha");
  assert.ok(persona.children[2].disabled, "inactive persona should not be selectable");
  persona.value = "jarvis";
  persona._listeners.get("change")();
  const request = JSON.parse(socket.sent.at(-1));
  assert.equal(request.type, "set_persona");
  assert.ok(persona.disabled, "selection should wait for confirmation");
  message({ type: "set_persona_ack", request_id: "another-request", id: "jarvis" });
  assert.ok(persona.disabled, "unrelated acknowledgement confirmed the request");
  message({ type: "persona_selected", selection: { persona_id: "jarvis", display_name: "Jarvis", revision: 4 } });
  message({ type: "set_persona_ack", request_id: request.request_id, id: "jarvis", display_name: "Jarvis" });
  assert.equal(persona.value, "jarvis");
  assert.equal(persona.disabled, false);
  assert.equal(env.elements.get("conversationTitle").textContent, "Conversation · Jarvis");
  message({ type: "response_ready", response: "duplicate", response_envelope: { response_id: "old" } });
  assert.equal(log.children.length, 2, "snapshot response was duplicated");
  message({ type: "response_delta", text: "Par", response_envelope: { response_id: "new", display_name: "Samantha" } });
  message({ type: "response_delta", text: "tial", response_envelope: { response_id: "new", display_name: "Samantha" } });
  assert.equal(log.children.at(-1).textContent, "Samantha: Partial");
  message({ type: "response_ready", response: "Canonical answer", response_envelope: { response_id: "new", display_name: "Samantha" } });
  assert.equal(log.children.at(-1).textContent, "Samantha: Canonical answer");
  assert.equal(log.children.length, 3, "final answer did not replace the streamed draft");
  message({ type: "session_snapshot", state_revision: 4, transcript: [] });
  assert.equal(log.children.length, 3, "stale snapshot erased conversation");

  env.sandbox.fetch = async (url) => {
    if (url === "/v1/work") return { status: 404 };
    if (url === "/v1/sessions") return { ok: true, json: async () => [{ id: "saved-1", summary: "A saved conversation" }] };
    if (url === "/v1/sessions/saved-1/resume") {
      message({ type: "session_snapshot", session_id: "saved-1", conversation_epoch: "new", state_revision: 1,
        selection: { persona_id: "samantha", display_name: "Samantha", revision: 1 },
        transcript: [{ role: "user", content: "Saved message" }] });
      return { ok: true, json: async () => ({ resumed: "saved-1" }) };
    }
    throw new Error("unexpected URL " + url);
  };
  const panel = env.elements.get("sessionsPanel");
  const sessions = env.elements.get("sessions");
  const resume = env.elements.get("resume");
  panel.open = true;
  panel._listeners.get("toggle")();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(sessions.children[1].textContent, "A saved conversation");
  sessions.value = "saved-1";
  sessions._listeners.get("change")();
  assert.equal(resume.disabled, false);
  resume.click();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(log.children.map((node) => node.textContent), ["You: Saved message"], "resume replaced the newly delivered snapshot");
  assert.equal(env.elements.get("conversationTitle").textContent, "Conversation · Samantha");
  const activity = env.elements.get("activity");
  message({ type: "tool_call_started", name: "read_file", summary: "notes.txt", work_scope: { session_id: "saved-1", conversation_epoch: "new" } });
  message({ type: "tool_call_finished", name: "read_file", status: "failed", message: "not found", work_scope: { session_id: "saved-1", conversation_epoch: "new" } });
  message({ type: "tool_call_started", name: "stale", work_scope: { session_id: "other", conversation_epoch: "old" } });
  const scope = { session_id: "saved-1", conversation_epoch: "new", runtime_id: "run" };
  message({ type: "work_state_changed", work: { scope, state_revision: 2, jobs: [{ key: { job_id: "job" }, task: "Research", state: "running" }] } });
  message({ type: "work_state_changed", work: { scope, state_revision: 1, jobs: [{ key: { job_id: "job" }, task: "Research", state: "queued" }] } });
  assert.deepEqual(activity.children.map((node) => node.textContent), ["Running read_file: notes.txt", "read_file · failed · not found", "Research · running"]);
  env.sandbox.fetch = async () => ({ ok: false, status: 422, json: async () => ({ error: "session unavailable" }) });
  sessions.value = "saved-1";
  resume.click();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(log.children[0].textContent, "You: Saved message", "failed resume erased the current transcript");
  assert.match(log.children.at(-1).textContent, /session unavailable/);

  const talk = env.elements.get("talk");
  const event = { preventDefault() {} };
  talk._listeners.get("mousedown")(event);
  await new Promise((resolve) => setImmediate(resolve));
  talk._listeners.get("mouseup")(event);
  releaseMic();
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(trackStopped, "released pending microphone stream was left open");
  assert.ok(!socket.sent.some((frame) => typeof frame === "string" && JSON.parse(frame).type === "voice_start"), "late microphone permission started a released turn");
}

async function exerciseWorkReconnect() {
  const env = makeEnvironment();
  let resolveWork;
  env.sandbox.fetch = async (url) => {
    if (url === "/v1/personas") return { ok: true, json: async () => ({ personas: [] }) };
    if (url === "/v1/work") return new Promise((resolve) => { resolveWork = resolve; });
    throw new Error("unexpected URL " + url);
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, "app.js"), "utf8"), env.sandbox, { filename: "app.js" });
  env.elements.get("start").click();
  await new Promise((resolve) => setImmediate(resolve));
  const socket = env.sockets.at(-1);
  socket.onopen();
  const message = (value) => socket.onmessage({ data: JSON.stringify(value) });
  const scope = { session_id: "saved", conversation_epoch: "epoch", runtime_id: "runtime" };
  message({ type: "session_snapshot", session_id: "saved", conversation_epoch: "epoch", state_revision: 1, transcript: [] });
  const work = (revision, state) => ({ scope, state_revision: revision, jobs: [{ key: { job_id: "job" }, task: "Research", state }] });
  message({ type: "work_state_changed", work: work(3, "running") });
  resolveWork({ ok: true, json: async () => ({ work: work(2, "queued") }) });
  await new Promise((resolve) => setImmediate(resolve));
  const activity = env.elements.get("activity");
  assert.deepEqual(activity.children.map((node) => node.textContent), ["Research · running"], "older HTTP state overwrote a newer event");

  env.elements.get("start").click();
  await new Promise((resolve) => setImmediate(resolve));
  const next = env.sockets.at(-1);
  next.onopen();
  next.onmessage({ data: JSON.stringify({ type: "session_snapshot", session_id: "saved", conversation_epoch: "epoch", state_revision: 1, transcript: [] }) });
  resolveWork({ ok: true, json: async () => ({ work: work(3, "running") }) });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(activity.children.map((node) => node.textContent), ["Research · running"], "reconnect did not restore current work");
}

function pcmJSON(responseID, generation, segmentID = 1) {
  return JSON.stringify({
    type: "audio_chunk",
    response_id: responseID,
    generation,
    segment_id: segmentID,
    sample_rate: 24000,
    data: Buffer.from([0, 1]).toString("base64"),
  });
}

function ova1() {
  const frame = new Uint8Array(18);
  frame.set(Buffer.from("OVA1"), 0);
  new DataView(frame.buffer).setUint32(4, 24000, true);
  new DataView(frame.buffer).setBigUint64(8, 1n, true);
  frame[16] = 0;
  frame[17] = 1;
  return frame.buffer;
}

function ova2(responseID, generation) {
  const id = Buffer.from(responseID);
  const frame = new Uint8Array(30 + id.length - 0);
  frame.set(Buffer.from("OVA2"), 0);
  const view = new DataView(frame.buffer);
  view.setUint32(4, 24000, true);
  view.setBigUint64(8, 1n, true);
  view.setBigUint64(16, BigInt(generation), true);
  view.setUint32(24, id.length, true);
  frame.set(id, 28);
  frame[28 + id.length] = 0;
  frame[29 + id.length] = 1;
  return frame.buffer;
}

async function exercise(file, connectID) {
  const env = makeEnvironment();
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, file), "utf8"), env.sandbox, { filename: file });
  env.elements.get(connectID).click();
  await Promise.resolve();
  await Promise.resolve();
  await new Promise((resolve) => setImmediate(resolve));
  const socket = env.sockets.at(-1);
  assert.ok(socket, `${file}: socket was not created (listeners=${Array.from(env.elements.get(connectID)._listeners.keys())}, status=${env.elements.get("status").textContent}, log=${env.elements.get("log").textContent})`);
  socket.onopen();

  // Two response IDs may share a generation. A control event for B must not
  // relabel or reject an A frame still queued on the audio channel.
  socket.onmessage({ data: pcmJSON("response-a", 1) });
  socket.onmessage({ data: JSON.stringify({ type: "thinking_started", response_envelope: { response_id: "response-b" } }) });
  socket.onmessage({ data: pcmJSON("response-a", 1, 2) });
  socket.onmessage({ data: pcmJSON("response-b", 1, 3) });
  assert.equal(env.sources.length, 3, `${file}: same-generation response audio was rejected`);

  socket.onmessage({ data: JSON.stringify({ type: "audio_end", response_id: "response-a", generation: 1, segment_id: 1, reason: "interrupted" }) });
  const stoppedBeforeReset = env.sources.filter((source) => source.stopped).length;
  socket.onmessage({ data: JSON.stringify({ type: "audio_reset" }) });
  const stoppedAtReset = env.sources.filter((source) => source.stopped).length;
  assert.ok(stoppedAtReset > stoppedBeforeReset, `${file}: reset did not stop prior sources`);
  socket.onmessage({ data: pcmJSON("response-a", 1, 4) });
  assert.equal(env.sources.length, 3, `${file}: stale post-reset chunk was accepted`);
  socket.onmessage({ data: pcmJSON("response-b", 3, 5) });
  assert.equal(env.sources.length, 4, `${file}: resumed generation was rejected`);

  socket.onmessage({ data: ova1() });
  socket.onmessage({ data: ova2("response-c", 3) });
  assert.equal(env.sources.length, 6, `${file}: OVA1/OVA2 behavior failed`);

  // A reconnect starts a fresh local fence, allowing a restarted server's
  // generation 1 to be accepted after this page had seen generation 3.
  env.elements.get(connectID).click();
  await Promise.resolve();
  await Promise.resolve();
  await new Promise((resolve) => setImmediate(resolve));
  const fresh = env.sockets.at(-1);
  fresh.onopen();
  fresh.onmessage({ data: ova2("response-fresh", 1) });
  assert.equal(env.sources.length, 7, `${file}: reconnect generation 1 was suppressed`);
}

(async () => {
  await exercise("app.js", "start");
  await exercise("stage.js", "connect");
  await exerciseConversation();
  await exerciseWorkReconnect();
  console.log("audio wire browser behavior: pass (app.js, stage.js)");
})().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
