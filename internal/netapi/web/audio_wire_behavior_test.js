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
  return {
    value: "token",
    textContent: "",
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
    appendChild() {},
    focus() {},
    _listeners: listeners,
  };
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
  const ids = ["token", "pairCode", "pair", "start", "talk", "send", "interrupt", "clear", "input", "log", "status", "gl", "fx", "pill", "hint", "sheet", "gear", "connect", "demo"];
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
  console.log("audio wire browser behavior: pass (app.js, stage.js)");
})().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
