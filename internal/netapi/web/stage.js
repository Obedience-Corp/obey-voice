/* Camera-facing voice stage. Demo by default; optional live /v1/stream. */
(function () {
  const TOKEN_KEY = "samantha.serve.token";
  const SKIN_KEY = "obey.stage.skin";
  const CAPTURE_RATE = 16000;
  const SKINS = ["orb", "radial", "wave"];
  const VAD = { on: 0.08, off: 0.04, startMs: 160, endMs: 1200, bargeMs: 260 };

  const $ = (id) => document.getElementById(id);
  const glCanvas = $("gl");
  const fxCanvas = $("fx");
  const pill = $("pill");
  const hint = $("hint");
  const sheet = $("sheet");
  const gear = $("gear");

  const gl = glCanvas.getContext("webgl", { antialias: true, alpha: false });
  const fx = fxCanvas.getContext("2d");

  const COLORS = {
    idle:     { a: [0.22, 0.28, 0.55], b: [0.48, 0.64, 0.97] },
    listening:{ a: [0.12, 0.42, 0.38], b: [0.48, 0.90, 0.78] },
    thinking: { a: [0.42, 0.28, 0.10], b: [0.95, 0.72, 0.38] },
    speaking: { a: [0.48, 0.16, 0.38], b: [0.97, 0.48, 0.62] },
  };
  const STATE_ID = { idle: 0, listening: 1, thinking: 2, speaking: 3 };

  const DEMO_LINES = [
    { who: "You", text: "What is this thing actually doing?" },
    { who: "Obey", text: "Talking. The thought is still finishing. Nothing left the machine." },
    { who: "You", text: "So the audio stays here?" },
    { who: "Obey", text: "Mic, voice, reply. All local. I start speaking before the sentence is done." },
  ];

  let skin = SKINS.includes(localStorage.getItem(SKIN_KEY))
    ? localStorage.getItem(SKIN_KEY)
    : "orb";
  let mode = "demo"; // demo | live
  let state = "idle";
  let energyIn = 0;
  let energyOut = 0;
  let energy = 0;
  let shock = 0;
  let hintGone = false;
  let connected = false;
  let talking = false;
  let armed = false;
  let speechMs = 0;
  let silenceMs = 0;
  let bargeMs = 0;
  let earHoldoffUntil = 0;
  let t0 = performance.now();
  let demoTimer = 0;
  let demoLine = 0;
  let puppet = false;

  let ws = null;
  let audioCtx = null;
  let mediaStream = null;
  let analyser = null;
  let captureNode = null;
  let captureSource = null;
  let nextPlayTime = 0;
  let audioSuppressed = true;
  const sourcesBySegment = new Map();
  const canceledSegments = new Set();
  let playbackAnalyser = null;
  let playbackDest = null;

  const particles = Array.from({ length: 90 }, () => ({
    a: Math.random() * Math.PI * 2,
    r: 0.18 + Math.random() * 0.55,
    s: 0.12 + Math.random() * 0.45,
    z: 0.4 + Math.random() * 0.6,
  }));

  // --- WebGL orb ----------------------------------------------------------
  let program = null;
  let buf = null;
  let u = {};

  function compile(type, src) {
    const sh = gl.createShader(type);
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
      console.warn(gl.getShaderInfoLog(sh));
      return null;
    }
    return sh;
  }

  function initGL() {
    if (!gl) return;
    const vs = compile(
      gl.VERTEX_SHADER,
      "attribute vec2 a; void main(){ gl_Position = vec4(a,0.0,1.0); }"
    );
    const fs = compile(
      gl.FRAGMENT_SHADER,
      [
        "precision highp float;",
        "uniform vec2 r;",
        "uniform float t;",
        "uniform float energy;",
        "uniform float st;",
        "uniform float shock;",
        "uniform vec3 c1;",
        "uniform vec3 c2;",
        "float hash(vec2 p){ return fract(sin(dot(p, vec2(127.1,311.7))) * 43758.5453); }",
        "float noise(vec2 p){",
        "  vec2 i=floor(p), f=fract(p);",
        "  f=f*f*(3.0-2.0*f);",
        "  return mix(mix(hash(i),hash(i+vec2(1,0)),f.x), mix(hash(i+vec2(0,1)),hash(i+vec2(1,1)),f.x), f.y);",
        "}",
        "void main(){",
        "  vec2 uv=(gl_FragCoord.xy - 0.5*r)/min(r.x,r.y);",
        "  float think=step(1.5,st)*step(st,2.5);",
        "  float ang=t*(0.12+st*0.18+think*1.6);",
        "  float ca=cos(ang), sa=sin(ang);",
        "  vec2 p=mat2(ca,-sa,sa,ca)*uv;",
        "  float n=noise(p*3.1+t*0.35)*0.55 + noise(p*8.0-t*0.22)*0.28;",
        "  float rad=0.26 + energy*0.16 + 0.025*sin(t*1.7) + shock*0.12 - think*0.03;",
        "  float d=length(p)-rad-n*(0.045+energy*0.09);",
        "  float glow=exp(-5.5*abs(d)) + 0.55*exp(-16.0*max(d,0.0));",
        "  float core=smoothstep(0.07,-0.04,d);",
        "  vec3 col=mix(c1,c2, clamp(n+energy*0.5,0.0,1.0));",
        "  float ring=exp(-40.0*abs(length(uv)-(0.42+shock*0.35)));",
        "  float orbit=exp(-70.0*abs(length(uv)-(0.36+0.05*sin(t*7.0))));",
        "  col = col*(glow*(0.5+energy)+core*0.95) + vec3(0.9,0.95,1.0)*ring*shock;",
        "  col += vec3(1.0,0.78,0.38)*orbit*think*(0.55+0.45*sin(t*8.0));",
        "  float vig=1.0-0.42*dot(uv,uv);",
        "  gl_FragColor=vec4(col*vig,1.0);",
        "}",
      ].join("\n")
    );
    if (!vs || !fs) return;
    program = gl.createProgram();
    gl.attachShader(program, vs);
    gl.attachShader(program, fs);
    gl.linkProgram(program);
    buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
    const loc = gl.getAttribLocation(program, "a");
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
    u = {
      r: gl.getUniformLocation(program, "r"),
      t: gl.getUniformLocation(program, "t"),
      energy: gl.getUniformLocation(program, "energy"),
      st: gl.getUniformLocation(program, "st"),
      shock: gl.getUniformLocation(program, "shock"),
      c1: gl.getUniformLocation(program, "c1"),
      c2: gl.getUniformLocation(program, "c2"),
    };
  }

  function resize() {
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    for (const c of [glCanvas, fxCanvas]) {
      c.width = Math.floor(window.innerWidth * dpr);
      c.height = Math.floor(window.innerHeight * dpr);
    }
    if (gl) gl.viewport(0, 0, glCanvas.width, glCanvas.height);
  }

  function setState(next, meta) {
    if (next !== state) {
      state = next;
      if (next === "idle" && mode === "demo" && !talking) {
        /* keep last caption */
      }
    }
    const label = mode === "live" && connected ? "live" : "demo";
    pill.textContent = next + " · " + label;
    if (meta && meta.who && meta.text) showCaption(meta.who, meta.text);
  }

  function showCaption() {
    /* Stage is a voice-only camera face. Transcript lives in the TUI. */
  }

  function hideHint() {
    if (hintGone) return;
    hintGone = true;
    hint.classList.add("gone");
  }

  function lerp(a, b, t) {
    return a + (b - a) * t;
  }

  function drawRadial(now) {
    const w = fxCanvas.width;
    const h = fxCanvas.height;
    const cx = w / 2;
    const cy = h / 2;
    const min = Math.min(w, h);
    const bars = 64;
    fx.clearRect(0, 0, w, h);
    for (let i = 0; i < bars; i++) {
      const a = (i / bars) * Math.PI * 2 + now * 0.15;
      const pulse = 0.35 + energy * 0.9 + 0.15 * Math.sin(now * 3 + i);
      const inner = min * 0.16;
      const outer = inner + min * (0.08 + pulse * 0.12);
      fx.strokeStyle = "rgba(180,210,255," + (0.25 + energy * 0.55) + ")";
      fx.lineWidth = Math.max(2, min * 0.006);
      fx.beginPath();
      fx.moveTo(cx + Math.cos(a) * inner, cy + Math.sin(a) * inner);
      fx.lineTo(cx + Math.cos(a) * outer, cy + Math.sin(a) * outer);
      fx.stroke();
    }
    drawParticles(now, cx, cy, min);
    drawThinkOrbit(now, cx, cy, min);
    drawShock(cx, cy, min);
  }

  function drawWave(now) {
    const w = fxCanvas.width;
    const h = fxCanvas.height;
    fx.clearRect(0, 0, w, h);
    fx.lineWidth = Math.max(3, h * 0.008);
    fx.strokeStyle = "rgba(170, 210, 255, 0.85)";
    fx.beginPath();
    const mid = h * 0.52;
    const amp = h * (0.04 + energy * 0.16);
    for (let x = 0; x <= w; x += 4) {
      const n = x / w;
      const y =
        mid +
        Math.sin(n * 12 + now * 2.4) * amp * (0.6 + energy) +
        Math.sin(n * 31 - now * 1.6) * amp * 0.35;
      if (x === 0) fx.moveTo(x, y);
      else fx.lineTo(x, y);
    }
    fx.stroke();
    drawParticles(now, w / 2, h / 2, Math.min(w, h));
    drawThinkOrbit(now, w / 2, h / 2, Math.min(w, h));
    drawShock(w / 2, h / 2, Math.min(w, h));
  }

  function drawParticles(now, cx, cy, min) {
    const pal = COLORS[state] || COLORS.idle;
    for (const p of particles) {
      const spin = p.a + now * p.s * (0.3 + STATE_ID[state] * 0.15);
      const rad = min * (0.22 + p.r * (0.28 + energy * 0.22));
      const x = cx + Math.cos(spin) * rad;
      const y = cy + Math.sin(spin) * rad * 0.92;
      const alpha = 0.15 + p.z * (0.25 + energy * 0.5);
      fx.fillStyle =
        "rgba(" +
        Math.floor(pal.b[0] * 255) +
        "," +
        Math.floor(pal.b[1] * 255) +
        "," +
        Math.floor(pal.b[2] * 255) +
        "," +
        alpha +
        ")";
      fx.beginPath();
      fx.arc(x, y, Math.max(1.2, min * 0.0035 * p.z * (1 + energy)), 0, Math.PI * 2);
      fx.fill();
    }
  }

  function drawShock(cx, cy, min) {
    if (shock < 0.02) return;
    fx.strokeStyle = "rgba(255,255,255," + shock * 0.7 + ")";
    fx.lineWidth = Math.max(2, min * 0.008 * shock);
    fx.beginPath();
    fx.arc(cx, cy, min * (0.2 + (1 - shock) * 0.38), 0, Math.PI * 2);
    fx.stroke();
  }

  function drawThinkOrbit(now, cx, cy, min) {
    if (state !== "thinking") return;
    const n = 3;
    for (let i = 0; i < n; i++) {
      const a = now * 2.8 + (i * Math.PI * 2) / n;
      const rad = min * 0.2;
      const x = cx + Math.cos(a) * rad;
      const y = cy + Math.sin(a) * rad * 0.92;
      fx.fillStyle = "rgba(255, 196, 92, 0.9)";
      fx.beginPath();
      fx.arc(x, y, Math.max(3, min * 0.01), 0, Math.PI * 2);
      fx.fill();
    }
  }

  function drawOrbOverlay(now) {
    const w = fxCanvas.width;
    const h = fxCanvas.height;
    fx.clearRect(0, 0, w, h);
    drawParticles(now, w / 2, h / 2, Math.min(w, h));
    drawThinkOrbit(now, w / 2, h / 2, Math.min(w, h));
    drawShock(w / 2, h / 2, Math.min(w, h));
  }

  function render(ts) {
    const now = (ts - t0) / 1000;
    const target = Math.max(energyIn, energyOut) + (state === "thinking" ? 0.22 : 0);
    energy = lerp(energy, target, 0.12);
    shock *= 0.9;
    sampleMeters();

    if (gl && program && skin === "orb") {
      const pal = COLORS[state] || COLORS.idle;
      gl.useProgram(program);
      gl.uniform2f(u.r, glCanvas.width, glCanvas.height);
      gl.uniform1f(u.t, now);
      gl.uniform1f(u.energy, energy);
      gl.uniform1f(u.st, STATE_ID[state] || 0);
      gl.uniform1f(u.shock, shock);
      gl.uniform3fv(u.c1, pal.a);
      gl.uniform3fv(u.c2, pal.b);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
      drawOrbOverlay(now);
    } else if (skin === "radial") {
      fillBackdrop();
      drawRadial(now);
    } else {
      fillBackdrop();
      drawWave(now);
    }
    requestAnimationFrame(render);
  }

  function fillBackdrop() {
    if (!gl || !program) {
      fx.fillStyle = "#05060a";
      fx.fillRect(0, 0, fxCanvas.width, fxCanvas.height);
      return;
    }
    const pal = COLORS[state] || COLORS.idle;
    gl.useProgram(program);
    gl.uniform2f(u.r, glCanvas.width, glCanvas.height);
    gl.uniform1f(u.t, (performance.now() - t0) / 1000);
    gl.uniform1f(u.energy, energy * 0.25);
    gl.uniform1f(u.st, STATE_ID[state] || 0);
    gl.uniform1f(u.shock, shock * 0.3);
    gl.uniform3fv(u.c1, pal.a.map((x) => x * 0.35));
    gl.uniform3fv(u.c2, pal.b.map((x) => x * 0.35));
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }

  function sampleMeters() {
    if (analyser) {
      const arr = new Uint8Array(analyser.fftSize);
      analyser.getByteTimeDomainData(arr);
      let sum = 0;
      for (let i = 0; i < arr.length; i++) {
        const v = (arr[i] - 128) / 128;
        sum += v * v;
      }
      const rms = Math.sqrt(sum / arr.length);
      energyIn = armed ? Math.min(1, rms * 4) : energyIn * 0.85;
    } else if (!talking) {
      energyIn *= 0.85;
    }
    if (playbackAnalyser && state === "speaking") {
      const arr = new Uint8Array(playbackAnalyser.fftSize);
      playbackAnalyser.getByteTimeDomainData(arr);
      let sum = 0;
      for (let i = 0; i < arr.length; i++) {
        const v = (arr[i] - 128) / 128;
        sum += v * v;
      }
      energyOut = Math.min(1, Math.sqrt(sum / arr.length) * 3.2);
    } else if (state === "speaking") {
      energyOut = 0.35 + 0.2 * Math.sin(performance.now() / 140);
    } else {
      energyOut *= 0.88;
    }
  }

  // --- audio / live -------------------------------------------------------
  function ensureAudio() {
    if (!audioCtx) {
      const Ctx = window.AudioContext || window.webkitAudioContext;
      audioCtx = new Ctx();
      playbackAnalyser = audioCtx.createAnalyser();
      playbackAnalyser.fftSize = 512;
      playbackDest = audioCtx.createGain();
      playbackDest.gain.value = 1;
      playbackDest.connect(playbackAnalyser);
      playbackAnalyser.connect(audioCtx.destination);
    }
    if (audioCtx.state === "suspended") return audioCtx.resume();
    return Promise.resolve();
  }

  async function ensureMic() {
    if (mediaStream) return mediaStream;
    mediaStream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
      video: false,
    });
    await ensureAudio();
    const src = audioCtx.createMediaStreamSource(mediaStream);
    analyser = audioCtx.createAnalyser();
    analyser.fftSize = 512;
    src.connect(analyser);
    return mediaStream;
  }

  function floatTo16BitPCM(float32) {
    const buf = new ArrayBuffer(float32.length * 2);
    const view = new DataView(buf);
    for (let i = 0; i < float32.length; i++) {
      let s = Math.max(-1, Math.min(1, float32[i]));
      view.setInt16(i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    }
    const bytes = new Uint8Array(buf);
    let binary = "";
    for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
    return btoa(binary);
  }

  function downsample(buffer, fromRate, toRate) {
    if (fromRate === toRate) return buffer;
    const ratio = fromRate / toRate;
    const newLen = Math.round(buffer.length / ratio);
    const result = new Float32Array(newLen);
    for (let i = 0; i < newLen; i++) result[i] = buffer[Math.floor(i * ratio)] || 0;
    return result;
  }

  function sendJSON(obj) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(obj));
  }

  function rmsOf(float32) {
    let sum = 0;
    for (let i = 0; i < float32.length; i++) sum += float32[i] * float32[i];
    return Math.sqrt(sum / Math.max(1, float32.length));
  }

  function startCaptureLoop() {
    if (captureNode || !audioCtx || !mediaStream) return;
    captureSource = audioCtx.createMediaStreamSource(mediaStream);
    captureNode = audioCtx.createScriptProcessor(4096, 1, 1);
    let last = performance.now();
    captureNode.onaudioprocess = (e) => {
      const input = e.inputBuffer.getChannelData(0);
      const now = performance.now();
      const dt = Math.min(80, now - last);
      last = now;
      const rms = rmsOf(input);
      stepVad(rms, dt);
      if (!talking || !connected || !ws || ws.readyState !== WebSocket.OPEN) return;
      const rate = e.inputBuffer.sampleRate || audioCtx.sampleRate;
      const mono = downsample(input, rate, CAPTURE_RATE);
      if (mono.length === 0) return;
      sendJSON({
        type: "audio_input",
        sample_rate: CAPTURE_RATE,
        data: floatTo16BitPCM(mono),
      });
    };
    captureSource.connect(captureNode);
    const mute = audioCtx.createGain();
    mute.gain.value = 0;
    captureNode.connect(mute);
    mute.connect(audioCtx.destination);
  }

  async function openEar() {
    await ensureAudio();
    try {
      await ensureMic();
    } catch (err) {
      showCaption("Stage", "Mic blocked — tap once and allow it.");
      if (hint) {
        hint.textContent = "tap once, then just talk";
        hint.classList.remove("gone");
        hintGone = false;
      }
      return false;
    }
    startCaptureLoop();
    armed = true;
    if (hint) {
      hint.textContent = "just talk · tap to cut · swipe to change face";
      hint.classList.remove("gone");
      hintGone = false;
      setTimeout(hideHint, 4500);
    }
    if (state === "idle") showCaption("Stage", "Listening. Just talk.");
    return true;
  }

  function beginUtterance() {
    if (talking) return;
    talking = true;
    speechMs = 0;
    silenceMs = 0;
    setState("listening");
    if (connected) sendJSON({ type: "voice_start" });
  }

  function endUtterance() {
    if (!talking) return;
    talking = false;
    speechMs = 0;
    silenceMs = 0;
    if (connected) {
      sendJSON({ type: "voice_end" });
      setState("thinking");
    } else {
      runDemoReply();
    }
  }

  function stepVad(rms, dt) {
    if (!armed || puppet) return;
    // Do not barge-in from the mic while the agent is thinking or talking.
    // Browser echo cancellation is not good enough; the orb was hanging up
    // on its own voice (or room noise) before a reply could play. Tap cuts.
    if (state === "speaking" || state === "thinking") {
      bargeMs = 0;
      return;
    }
    if (performance.now() < earHoldoffUntil) return;
    bargeMs = 0;
    if (rms > VAD.on) {
      speechMs += dt;
      silenceMs = 0;
      if (!talking && speechMs >= VAD.startMs) beginUtterance();
    } else {
      speechMs = 0;
      if (talking && rms < VAD.off) {
        silenceMs += dt;
        if (silenceMs >= VAD.endMs) endUtterance();
      }
    }
  }

  function interrupt() {
    hideHint();
    shock = 1;
    talking = false;
    speechMs = 0;
    silenceMs = 0;
    bargeMs = 0;
    if (connected) sendJSON({ type: "interrupt" });
    stopAudio(false);
    setState("idle");
  }

  function segmentKey(id) {
    return id == null ? "unknown" : String(id);
  }

  function forgetSource(key, src) {
    const sources = sourcesBySegment.get(key);
    if (!sources) return;
    sources.delete(src);
    if (sources.size === 0) sourcesBySegment.delete(key);
  }

  function stopSegment(key, markCanceled) {
    if (markCanceled) canceledSegments.add(key);
    const sources = sourcesBySegment.get(key);
    if (!sources) return;
    for (const src of sources) {
      src.onended = null;
      try { src.stop(); } catch (_) {}
      try { src.disconnect(); } catch (_) {}
    }
    sourcesBySegment.delete(key);
  }

  function stopAudio(suppress) {
    if (suppress) audioSuppressed = true;
    for (const key of Array.from(sourcesBySegment.keys())) stopSegment(key, true);
    nextPlayTime = audioCtx ? audioCtx.currentTime : 0;
  }

  function playPCM(base64, sampleRate, segmentID) {
    const key = segmentKey(segmentID);
    if (!base64 || audioSuppressed || canceledSegments.has(key)) return;
    if (!audioCtx) {
      ensureAudio().catch(() => {});
      if (!audioCtx) return;
    }
    if (audioCtx.state === "suspended") audioCtx.resume().catch(() => {});
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    const view = new DataView(bytes.buffer);
    const n = Math.floor(bytes.length / 2);
    if (n === 0) return;
    const floats = new Float32Array(n);
    for (let i = 0; i < n; i++) floats[i] = view.getInt16(i * 2, true) / 32768;
    const rate = sampleRate || 24000;
    let buffer;
    try {
      buffer = audioCtx.createBuffer(1, floats.length, rate);
      buffer.copyToChannel(floats, 0);
    } catch (err) {
      showCaption("Stage", "Bad audio chunk: " + err.message);
      return;
    }
    const src = audioCtx.createBufferSource();
    src.buffer = buffer;
    src.connect(playbackDest || audioCtx.destination);
    let sources = sourcesBySegment.get(key);
    if (!sources) {
      sources = new Set();
      sourcesBySegment.set(key, sources);
    }
    sources.add(src);
    src.onended = () => forgetSource(key, src);
    const startAt = Math.max(nextPlayTime, audioCtx.currentTime + 0.02);
    try {
      src.start(startAt);
    } catch (err) {
      showCaption("Stage", "Playback failed: " + err.message);
      return;
    }
    nextPlayTime = startAt + buffer.duration;
    setState("speaking");
  }

  function connectLive() {
    const token = $("token").value.trim() || localStorage.getItem(TOKEN_KEY) || "";
    if (!token) {
      showCaption("Stage", "Pair or paste a token first.");
      return;
    }
    localStorage.setItem(TOKEN_KEY, token);
    $("token").value = token;
    stopAudio(true);
    canceledSegments.clear();
    if (ws) {
      const prev = ws;
      ws = null;
      try { prev.close(); } catch (_) {}
    }
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const host = location.host || "127.0.0.1:8787";
    const url = proto + "//" + host + "/v1/stream?token=" + encodeURIComponent(token);
    const socket = new WebSocket(url);
    ws = socket;
    pill.textContent = "connecting…";
    socket.onopen = () => {
      if (ws !== socket) { socket.close(); return; }
      connected = true;
      mode = "live";
      audioSuppressed = false;
      nextPlayTime = 0;
      socket.send(JSON.stringify({ type: "audio_output", mode: "stream" }));
      setState("idle");
      pill.classList.add("live");
      closeSheet();
      showCaption("Stage", "Live. Just talk.");
      ensureAudio()
        .then(() => openEar())
        .catch(() => {});
    };
    socket.onclose = () => {
      if (ws !== socket) return;
      ws = null;
      connected = false;
      mode = "demo";
      stopAudio(true);
      setState("idle");
      pill.classList.remove("live");
    };
    socket.onmessage = (ev) => {
      if (ws !== socket) return;
      let env;
      try { env = JSON.parse(ev.data); } catch (_) { return; }
      switch (env.type) {
        case "user_input":
          showCaption("You", env.text || "");
          break;
        case "thinking_started":
          setState("thinking");
          break;
        case "response_ready":
          break;
        case "error":
          showCaption("Stage", env.message || JSON.stringify(env));
          setState("idle");
          break;
        case "generating_voice":
        case "speaking_started":
          setState("speaking");
          break;
        case "speaking_complete":
          earHoldoffUntil = performance.now() + 500;
          setState("idle");
          break;
        case "audio_level":
          if (env.source === "input") energyIn = env.level || 0;
          if (env.source === "output") energyOut = env.level || 0;
          break;
        case "audio_chunk":
          playPCM(env.data, env.sample_rate, env.segment_id);
          break;
        case "audio_reset":
          stopAudio(false);
          canceledSegments.clear();
          audioSuppressed = false;
          break;
        case "turn_interrupted":
        case "speaking_interrupted":
          shock = 1;
          stopAudio(false);
          setState("idle");
          break;
        default:
          break;
      }
    };
  }

  function pair() {
    const code = $("pairCode").value.trim();
    if (!code) {
      showCaption("Stage", "Need a pairing code.");
      return Promise.resolve(false);
    }
    return fetch("/v1/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: code }),
    })
      .then(async (resp) => {
        const body = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(body.error || "pair failed");
        if (!body.token) throw new Error("no token");
        $("token").value = body.token;
        localStorage.setItem(TOKEN_KEY, body.token);
        $("pairCode").value = "";
        connectLive();
        return true;
      })
      .catch((err) => {
        showCaption("Stage", "Pair failed: " + err.message);
        return false;
      });
  }

  // --- demo ---------------------------------------------------------------
  function runDemoReply() {
    setState("thinking");
    const line = DEMO_LINES[demoLine % DEMO_LINES.length];
    demoLine += 1;
    clearTimeout(demoTimer);
    demoTimer = setTimeout(() => {
      setState("speaking", { who: line.who === "You" ? "Obey" : line.who, text: line.text });
      hum(Math.min(4200, 80 * line.text.length));
      demoTimer = setTimeout(() => setState("idle"), Math.min(4200, 80 * line.text.length));
    }, 700);
  }

  function hum(ms) {
    if (!audioCtx) return;
    const osc = audioCtx.createOscillator();
    const filt = audioCtx.createBiquadFilter();
    const gain = audioCtx.createGain();
    osc.type = "sawtooth";
    osc.frequency.value = 92;
    filt.type = "lowpass";
    filt.frequency.value = 420;
    gain.gain.value = 0.0001;
    osc.connect(filt);
    filt.connect(gain);
    gain.connect(playbackDest || audioCtx.destination);
    const now = audioCtx.currentTime;
    gain.gain.exponentialRampToValueAtTime(0.04, now + 0.08);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + ms / 1000);
    osc.start(now);
    osc.stop(now + ms / 1000 + 0.05);
  }

  function autoDemo() {
    if (connected || talking || puppet) return;
    puppet = true;
    const beat = () => {
      if (connected || talking || !puppet) {
        puppet = false;
        return;
      }
      const line = DEMO_LINES[demoLine % DEMO_LINES.length];
      demoLine += 1;
      setState("listening", { who: "You", text: line.who === "You" ? line.text : DEMO_LINES[0].text });
      energyIn = 0.45;
      setTimeout(() => {
        if (connected || talking) return;
        setState("thinking");
        energyIn = 0;
        setTimeout(() => {
          if (connected || talking) return;
          const reply = DEMO_LINES[demoLine % DEMO_LINES.length];
          demoLine += 1;
          setState("speaking", { who: "Obey", text: reply.text });
          ensureAudio().then(() => hum(Math.min(3800, 70 * reply.text.length)));
          setTimeout(() => {
            if (!connected && !talking && puppet) setState("idle");
            setTimeout(beat, 1600);
          }, Math.min(3800, 70 * reply.text.length));
        }, 800);
      }, 1400);
    };
    beat();
  }

  // --- gestures -----------------------------------------------------------
  const ptr = { down: false, x: 0, y: 0, t: 0, moved: false };

  function onDown(e) {
    if (e.target.closest && e.target.closest("#sheet, .gear, .panel")) return;
    e.preventDefault();
    ptr.down = true;
    ptr.moved = false;
    ptr.x = e.clientX;
    ptr.y = e.clientY;
    ptr.t = performance.now();
  }

  function onMove(e) {
    if (!ptr.down) return;
    const dx = e.clientX - ptr.x;
    const dy = e.clientY - ptr.y;
    if (Math.hypot(dx, dy) > 28) ptr.moved = true;
  }

  function onUp(e) {
    if (!ptr.down) return;
    e.preventDefault();
    ptr.down = false;
    const dx = e.clientX - ptr.x;
    const dy = e.clientY - ptr.y;
    const dt = performance.now() - ptr.t;
    if (Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy)) {
      cycleSkin(dx < 0 ? 1 : -1);
      return;
    }
    if (dt < 280 && Math.hypot(dx, dy) < 24) {
      if (!armed) {
        puppet = false;
        openEar().catch(() => {});
        return;
      }
      if (state === "speaking" || state === "thinking") interrupt();
    }
  }

  function cycleSkin(dir) {
    hideHint();
    const i = SKINS.indexOf(skin);
    skin = SKINS[(i + dir + SKINS.length) % SKINS.length];
    localStorage.setItem(SKIN_KEY, skin);
    shock = 0.6;
    showCaption("Stage", skin);
  }

  let lastTap = 0;
  function onClick(e) {
    if (e.target.closest && e.target.closest("#sheet, .gear, .panel")) return;
    const now = performance.now();
    if (now - lastTap < 280) {
      const el = document.documentElement;
      if (!document.fullscreenElement && el.requestFullscreen) el.requestFullscreen().catch(() => {});
      else if (document.exitFullscreen) document.exitFullscreen().catch(() => {});
    }
    lastTap = now;
  }

  function openSheet() { sheet.classList.add("open"); }
  function closeSheet() { sheet.classList.remove("open"); }

  // --- boot ---------------------------------------------------------------
  $("token").value = localStorage.getItem(TOKEN_KEY) || "";
  gear.addEventListener("click", (e) => {
    e.stopPropagation();
    sheet.classList.toggle("open");
  });
  sheet.addEventListener("click", (e) => {
    if (e.target === sheet) closeSheet();
  });
  $("pair").addEventListener("click", pair);
  $("connect").addEventListener("click", () => {
    ensureAudio().then(connectLive);
  });
  $("demo").addEventListener("click", () => {
    closeSheet();
    puppet = false;
    openEar().catch(() => {});
  });

  window.addEventListener("pointerdown", onDown, { passive: false });
  window.addEventListener("pointermove", onMove, { passive: false });
  window.addEventListener("pointerup", onUp, { passive: false });
  window.addEventListener("pointercancel", onUp, { passive: false });
  window.addEventListener("click", onClick);
  window.addEventListener("resize", resize);
  window.addEventListener("orientationchange", resize);
  document.addEventListener("visibilitychange", () => {
    if (document.hidden && talking) endUtterance();
  });

  initGL();
  resize();
  requestAnimationFrame(render);

  const params = new URLSearchParams(location.search);
  const code = (params.get("code") || "").trim();
  if (code) {
    $("pairCode").value = code;
    pair();
  } else if ((params.get("live") || location.protocol === "https:") && $("token").value) {
    ensureAudio().then(connectLive);
  } else if (params.get("puppet") === "1") {
    autoDemo();
  } else {
    openEar().catch(() => {});
  }
})();
