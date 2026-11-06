/* Browser conversation client for obey-voice serve. */
(function () {
  const $ = (id) => document.getElementById(id);
  const tokenEl = $("token");
  const pairCodeEl = $("pairCode");
  const pairBtn = $("pair");
  const startBtn = $("start");
  const talkBtn = $("talk");
  const sendBtn = $("send");
  const interruptBtn = $("interrupt");
  const clearBtn = $("clear");
  const inputEl = $("input");
  const logEl = $("log");
  const statusEl = $("status");
  const setupEl = $("setup");
  const titleEl = $("conversationTitle");
  const personaEl = $("persona");
  const sessionsPanel = $("sessionsPanel");
  const sessionsEl = $("sessions");
  const resumeBtn = $("resume");
  const activityEl = $("activity");
  const activitySummary = $("activitySummary");

  const TOKEN_KEY = "samantha.serve.token";
  const CAPTURE_RATE = 16000;

  let ws = null;
  let activeToken = "";
  let audioCtx = null;
  let nextPlayTime = 0;
  let connected = false;
  let audioSuppressed = true;
  let audioGeneration = 0;
  let talking = false;
  let mediaStream = null;
  let captureNode = null;
  let captureSource = null;
  let capturePending = false;
  let micRequest = 0;
  let selectedName = "Agent";
  let selectedID = "";
  let selectionRevision = -1;
  let snapshotRevision = -1;
  let snapshotSession = "";
  let snapshotEpoch = "";
  let snapshotCount = 0;
  let activityCount = 0;
  let workRevision = -1;
  let workScope = "";
  const workStates = new Map();
  let personaRequest = null;
  let personaRequestSerial = 0;
  const pendingReplies = new Map();
  const completedReplies = new Set();
  const sourcesBySegment = new Map();
  const canceledSegments = new Set();

  tokenEl.value = localStorage.getItem(TOKEN_KEY) || "";

  function setStatus(text, live) {
    statusEl.textContent = text;
    statusEl.classList.toggle("live", !!live);
  }

  function log(text, cls) {
    const p = document.createElement("div");
    p.className = "msg " + (cls || "sys");
    p.textContent = text;
    logEl.appendChild(p);
    logEl.scrollTop = logEl.scrollHeight;
    return p;
  }

  function clearActivity() {
    if (activityEl) activityEl.textContent = "";
    activityCount = 0;
    workRevision = -1;
    workScope = "";
    workStates.clear();
    if (activitySummary) activitySummary.textContent = "Work and activity";
  }

  function addActivity(text) {
    if (!activityEl || !text) return;
    const entry = document.createElement("div");
    entry.className = "activity-entry";
    entry.textContent = text;
    activityEl.appendChild(entry);
    if (activityEl.children.length > 80) activityEl.removeChild(activityEl.children[0]);
    activityCount += 1;
    if (activitySummary) activitySummary.textContent = "Work and activity (" + activityCount + ")";
    activityEl.scrollTop = activityEl.scrollHeight;
  }

  function showWork(snapshot) {
    if (!snapshot || !snapshot.scope) return;
    const scope = snapshot.scope;
    if (snapshotSession && scope.session_id !== snapshotSession) return;
    if (snapshotEpoch && scope.conversation_epoch !== snapshotEpoch) return;
    const key = [scope.session_id, scope.runtime_id, scope.conversation_epoch].join(":");
    if (key !== workScope) {
      workScope = key;
      workRevision = -1;
      workStates.clear();
    }
    if (typeof snapshot.state_revision === "number" && snapshot.state_revision <= workRevision) return;
    workRevision = typeof snapshot.state_revision === "number" ? snapshot.state_revision : workRevision;
    for (const job of snapshot.jobs || []) {
      const id = job.key && job.key.job_id;
      if (!id) continue;
      const state = job.outcome && job.outcome.status || job.state || "updated";
      const label = (job.task || "Background work") + " · " + state;
      if (workStates.get(id) !== label) {
        workStates.set(id, label);
        addActivity(label);
      }
    }
  }

  async function refreshWork(socket, token) {
    try {
      const response = await fetch("/v1/work", { headers: { Authorization: "Bearer " + token } });
      if (response.status === 404) return; // Older servers have live events only.
      if (!response.ok) throw new Error("HTTP " + response.status);
      const body = await response.json();
      if (ws === socket) showWork(body.work);
    } catch (err) {
      if (ws === socket) addActivity("Current work unavailable: " + err.message);
    }
  }

  function activityInCurrentSession(env) {
    const scope = env.work_scope || env.response_envelope || {};
    if (snapshotSession && scope.session_id && scope.session_id !== snapshotSession) return false;
    if (snapshotEpoch && scope.conversation_epoch && scope.conversation_epoch !== snapshotEpoch) return false;
    return true;
  }

  function selectionName(selection) {
    return (selection && (selection.display_name || selection.persona_id)) || "Agent";
  }

  function setSelection(selection) {
    if (!selection) return;
    if (typeof selection.revision === "number" && selection.revision < selectionRevision) return;
    selectionRevision = typeof selection.revision === "number" ? selection.revision : selectionRevision;
    selectedName = selectionName(selection);
    selectedID = selection.persona_id || selection.id || selectedID;
    if (personaEl && selectedID && !personaRequest) personaEl.value = selectedID;
    if (titleEl) titleEl.textContent = "Conversation · " + selectedName;
  }

  async function loadPersonas(socket, token) {
    if (!personaEl) return;
    try {
      const response = await fetch("/v1/personas", { headers: { Authorization: "Bearer " + token } });
      if (!response.ok) throw new Error("HTTP " + response.status);
      const result = await response.json();
      if (ws !== socket) return;
      personaEl.textContent = "";
      for (const persona of result.personas || []) {
        const option = document.createElement("option");
        option.value = persona.id;
        option.textContent = persona.display_name || persona.id;
        option.disabled = !!persona.inactive;
        personaEl.appendChild(option);
        if (!selectedID && persona.active) selectedID = persona.id;
      }
      if (selectedID) personaEl.value = selectedID;
      personaEl.disabled = !(result.personas || []).some((persona) => !persona.inactive);
      if (result.warnings && result.warnings.length) log("Some personas need repair on the host.", "err");
    } catch (err) {
      if (ws !== socket) return;
      personaEl.disabled = true;
      log("Persona list unavailable: " + err.message, "err");
    }
  }

  async function loadSessions() {
    if (!sessionsEl || !connected || !ws) return;
    const socket = ws;
    sessionsEl.disabled = true;
    if (resumeBtn) resumeBtn.disabled = true;
    try {
      const response = await fetch("/v1/sessions", { headers: { Authorization: "Bearer " + activeToken } });
      if (!response.ok) throw new Error("HTTP " + response.status);
      const sessions = await response.json();
      if (ws !== socket) return;
      sessionsEl.textContent = "";
      const placeholder = document.createElement("option");
      placeholder.value = "";
      placeholder.textContent = sessions.length ? "Choose a conversation" : "No saved conversations";
      sessionsEl.appendChild(placeholder);
      for (const session of sessions) {
        const option = document.createElement("option");
        option.value = session.id;
        const when = session.updated_at ? new Date(session.updated_at) : null;
        option.textContent = (session.summary || session.id) + (when && !Number.isNaN(when.getTime()) ? " · " + when.toLocaleString() : "");
        sessionsEl.appendChild(option);
      }
      sessionsEl.value = "";
      sessionsEl.disabled = sessions.length === 0;
    } catch (err) {
      if (ws === socket) log("Saved conversations unavailable: " + err.message, "err");
    }
  }

  async function resumeSession() {
    if (!connected || !ws || !sessionsEl || !sessionsEl.value) return;
    const socket = ws;
    const id = sessionsEl.value;
    const before = snapshotCount;
    resumeBtn.disabled = true;
    sessionsEl.disabled = true;
    try {
      const response = await fetch("/v1/sessions/" + encodeURIComponent(id) + "/resume", {
        method: "POST", headers: { Authorization: "Bearer " + activeToken },
      });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error || "HTTP " + response.status);
      if (ws !== socket) return;
      if (snapshotCount === before) {
        logEl.textContent = "";
        pendingReplies.clear();
        completedReplies.clear();
        log("Conversation resumed. Reconnect if its history does not appear.", "sys");
      }
      if (sessionsPanel) sessionsPanel.open = false;
    } catch (err) {
      if (ws === socket) log("Could not resume conversation: " + err.message, "err");
    } finally {
      if (ws === socket) {
        sessionsEl.disabled = false;
        resumeBtn.disabled = !sessionsEl.value;
      }
    }
  }

  function finishPersonaRequest() {
    if (!personaRequest) return;
    clearTimeout(personaRequest.timer);
    personaRequest = null;
    if (personaEl) {
      personaEl.disabled = !connected;
      if (selectedID) personaEl.value = selectedID;
    }
  }

  function replyIdentity(env) {
    const identity = env.response_envelope || {};
    return { id: identity.response_id || env.response_id || "", name: identity.display_name || selectedName };
  }

  function restoreSnapshot(env) {
    if (!Array.isArray(env.transcript)) return;
    const sameEpoch = (env.session_id || "") === snapshotSession && (env.conversation_epoch || "") === snapshotEpoch;
    if (sameEpoch && typeof env.state_revision === "number" && env.state_revision < snapshotRevision) return;
    snapshotRevision = typeof env.state_revision === "number" ? env.state_revision : -1;
    snapshotCount += 1;
    if (!sameEpoch) {
      selectionRevision = -1;
      clearActivity();
    }
    snapshotSession = env.session_id || "";
    snapshotEpoch = env.conversation_epoch || "";
    setSelection(env.selection);
    logEl.textContent = "";
    pendingReplies.clear();
    completedReplies.clear();
    for (const turn of env.transcript) {
      if (!turn || !turn.content || (turn.role !== "user" && turn.role !== "assistant")) continue;
      const name = turn.role === "user" ? (turn.speaker || "You") : (turn.persona_display_name || turn.persona_id || "Agent");
      log(name + ": " + turn.content, turn.role === "user" ? "you" : "sam");
      if (turn.role === "assistant" && turn.response_id) completedReplies.add(turn.response_id);
    }
  }

  function streamReply(env) {
    if (!env.text) return;
    const reply = replyIdentity(env);
    if (reply.id && completedReplies.has(reply.id)) return;
    const key = reply.id || "legacy";
    let pending = pendingReplies.get(key);
    if (!pending) {
      pending = { node: log(reply.name + ": ", "sam"), text: "", name: reply.name };
      pendingReplies.set(key, pending);
    }
    pending.text += env.text;
    pending.node.textContent = pending.name + ": " + pending.text;
    logEl.scrollTop = logEl.scrollHeight;
  }

  function finishReply(env) {
    const reply = replyIdentity(env);
    if (reply.id && completedReplies.has(reply.id)) return;
    const key = reply.id || "legacy";
    const pending = pendingReplies.get(key);
    if (pending) {
      pending.node.textContent = reply.name + ": " + (env.response || "");
      pendingReplies.delete(key);
    } else if (env.response) {
      log(reply.name + ": " + env.response, "sam");
    }
    if (reply.id) completedReplies.add(reply.id);
  }

  function setConnected(on) {
    connected = on;
    inputEl.disabled = !on;
    sendBtn.disabled = !on;
    interruptBtn.disabled = !on;
    clearBtn.disabled = !on;
    talkBtn.disabled = !on;
    if (!on && personaEl) personaEl.disabled = true;
    if (!on && sessionsEl) sessionsEl.disabled = true;
    if (!on && resumeBtn) resumeBtn.disabled = true;
    if (!on) finishPersonaRequest();
    startBtn.textContent = on ? "Reconnect" : "Connect";
    if (setupEl) setupEl.open = !on;
    if (!on) stopTalk(true);
  }

  function floatTo16BitBytes(float32) {
    const buf = new ArrayBuffer(float32.length * 2);
    const view = new DataView(buf);
    for (let i = 0; i < float32.length; i++) {
      let s = Math.max(-1, Math.min(1, float32[i]));
      view.setInt16(i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    }
    return new Uint8Array(buf);
  }

  function downsample(buffer, fromRate, toRate) {
    if (fromRate === toRate) return buffer;
    const ratio = fromRate / toRate;
    const newLen = Math.round(buffer.length / ratio);
    const result = new Float32Array(newLen);
    for (let i = 0; i < newLen; i++) {
      const idx = Math.floor(i * ratio);
      result[i] = buffer[idx] || 0;
    }
    return result;
  }

  async function ensureMic() {
    if (mediaStream) return mediaStream;
    mediaStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        channelCount: 1,
        echoCancellation: true,
        noiseSuppression: true,
      },
      video: false,
    });
    return mediaStream;
  }

  async function startTalk() {
    if (!connected || talking || capturePending) return;
    capturePending = true;
    const request = ++micRequest;
    try {
      await ensureAudio();
      await ensureMic();
    } finally {
      capturePending = false;
    }
    if (request !== micRequest || !connected) {
      releaseMic();
      return;
    }
    talking = true;
    talkBtn.textContent = "Release…";
    talkBtn.classList.add("danger");
    sendJSON({ type: "voice_start" });
    log("listening (hold)…", "sys");

    const stream = mediaStream;
    captureSource = audioCtx.createMediaStreamSource(stream);
    // ScriptProcessor is deprecated but works in iOS Safari without a worklet file.
    const bufferSize = 4096;
    captureNode = audioCtx.createScriptProcessor(bufferSize, 1, 1);
    captureNode.onaudioprocess = (e) => {
      if (!talking || !ws || ws.readyState !== WebSocket.OPEN) return;
      const input = e.inputBuffer.getChannelData(0);
      const rate = e.inputBuffer.sampleRate || audioCtx.sampleRate;
      const mono = downsample(input, rate, CAPTURE_RATE);
      if (mono.length === 0) return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(floatTo16BitBytes(mono));
      }
    };
    captureSource.connect(captureNode);
    // Must connect to destination (or a silent gain) for processing to run.
    const mute = audioCtx.createGain();
    mute.gain.value = 0;
    captureNode.connect(mute);
    mute.connect(audioCtx.destination);
  }

  function stopTalk(silent) {
    micRequest += 1;
    if (!talking && !captureNode) return;
    talking = false;
    talkBtn.textContent = "Hold to talk";
    talkBtn.classList.remove("danger");
    if (captureNode) {
      try { captureNode.disconnect(); } catch (_) {}
      captureNode.onaudioprocess = null;
      captureNode = null;
    }
    if (captureSource) {
      try { captureSource.disconnect(); } catch (_) {}
      captureSource = null;
    }
    releaseMic();
    if (!silent && connected) {
      sendJSON({ type: "voice_end" });
      log("sent utterance", "sys");
    }
  }

  function releaseMic() {
    if (mediaStream && typeof mediaStream.getTracks === "function") {
      for (const track of mediaStream.getTracks()) track.stop();
    }
    mediaStream = null;
  }

  function ensureAudio() {
    if (!audioCtx) {
      const Ctx = window.AudioContext || window.webkitAudioContext;
      audioCtx = new Ctx();
    }
    if (audioCtx.state === "suspended") {
      return audioCtx.resume();
    }
    return Promise.resolve();
  }

  function segmentKey(responseID, segmentID) {
    return (responseID || "unknown") + ":" + (segmentID == null ? "unknown" : String(segmentID));
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
    for (const key of Array.from(sourcesBySegment.keys())) {
      stopSegment(key, true);
    }
    nextPlayTime = audioCtx ? audioCtx.currentTime : 0;
  }

  function applyAudioReset() {
    stopAudio(false);
    canceledSegments.clear();
    audioGeneration += 1;
    audioSuppressed = false;
  }

  function acceptAudioIdentity(generation) {
    const gen = Number(generation || 0);
    if (gen > 0 && gen < audioGeneration) return false;
    if (gen > audioGeneration) {
      stopAudio(false);
      canceledSegments.clear();
      audioGeneration = gen;
    }
    return true;
  }

  function playPCMBytes(bytes, sampleRate, segmentID, responseID, generation) {
    if (!acceptAudioIdentity(generation)) return;
    const key = segmentKey(responseID, segmentID);
    if (!audioCtx || !bytes || bytes.length < 2 || audioSuppressed || canceledSegments.has(key)) return;
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    const n = Math.floor(bytes.length / 2);
    if (n === 0) return;
    const floats = new Float32Array(n);
    for (let i = 0; i < n; i++) {
      floats[i] = view.getInt16(i * 2, true) / 32768;
    }
    const rate = sampleRate || 24000;
    const buffer = audioCtx.createBuffer(1, floats.length, rate);
    buffer.copyToChannel(floats, 0);
    const src = audioCtx.createBufferSource();
    src.buffer = buffer;
    src.connect(audioCtx.destination);
    let sources = sourcesBySegment.get(key);
    if (!sources) {
      sources = new Set();
      sourcesBySegment.set(key, sources);
    }
    sources.add(src);
    src.onended = () => forgetSource(key, src);
    const startAt = Math.max(nextPlayTime, audioCtx.currentTime + 0.02);
    src.start(startAt);
    nextPlayTime = startAt + buffer.duration;
  }

  function playPCM(base64, sampleRate, segmentID, responseID, generation) {
    if (!base64) return;
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    playPCMBytes(bytes, sampleRate, segmentID, responseID, generation);
  }

  function playBinaryChunk(buf) {
    const bytes = new Uint8Array(buf);
    if (bytes.length < 16) return;
    const magic = String.fromCharCode(bytes[0], bytes[1], bytes[2], bytes[3]);
    if (magic === "OVA2") {
      if (bytes.length < 28) return;
      const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
      const idLen = view.getUint32(24, true);
      if (28 + idLen > bytes.length) return;
      const idBytes = bytes.subarray(28, 28 + idLen);
      const responseID = new TextDecoder().decode(idBytes);
      playPCMBytes(bytes.subarray(28 + idLen), view.getUint32(4, true), Number(view.getBigUint64(8, true)), responseID, Number(view.getBigUint64(16, true)));
      return;
    }
    if (magic !== "OVA1") return;
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    playPCMBytes(bytes.subarray(16), view.getUint32(4, true), Number(view.getBigUint64(8, true)), "", 0);
  }

  function sendJSON(obj) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify(obj));
  }

  function connect() {
    const token = tokenEl.value.trim();
    if (!token) {
      log("paste the serve token first", "err");
      return;
    }
    localStorage.setItem(TOKEN_KEY, token);

    setConnected(false);
    selectedID = "";
    selectedName = "Agent";
    if (titleEl) titleEl.textContent = "Conversation";
    logEl.textContent = "";
    pendingReplies.clear();
    completedReplies.clear();
    clearActivity();
    stopAudio(true);
    canceledSegments.clear();
    audioGeneration = 0;
    snapshotRevision = -1;
    selectionRevision = -1;
    snapshotSession = "";
    snapshotEpoch = "";
    snapshotCount = 0;
    if (ws) {
      const previous = ws;
      ws = null;
      try { previous.close(); } catch (_) {}
    }

    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url =
      proto +
      "//" +
      location.host +
      "/v1/stream?token=" +
      encodeURIComponent(token);

    setStatus("connecting…");
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    ws = socket;

    socket.onopen = () => {
      if (ws !== socket) {
        socket.close();
        return;
      }
      setConnected(true);
      activeToken = token;
      setStatus("connected", true);
      loadPersonas(socket, token);
      if (sessionsPanel && sessionsPanel.open) loadSessions();
      audioSuppressed = false;
      nextPlayTime = 0;
      socket.send(JSON.stringify({ type: "audio_output", mode: "stream", binary: true, protocol: "ova2" }));
    };

    socket.onclose = () => {
      if (ws !== socket) return;
      ws = null;
      activeToken = "";
      stopAudio(true);
      setConnected(false);
      setStatus("disconnected");
      log("disconnected", "sys");
    };

    socket.onerror = () => {
      if (ws !== socket) return;
      log("websocket error", "err");
    };

    socket.onmessage = (ev) => {
      if (ws !== socket) return;
      if (ev.data instanceof ArrayBuffer) {
        playBinaryChunk(ev.data);
        return;
      }
      let env;
      try {
        env = JSON.parse(ev.data);
      } catch (_) {
        return;
      }
      switch (env.type) {
        case "session_snapshot":
          restoreSnapshot(env);
          refreshWork(socket, token);
          break;
        case "persona_selected":
          setSelection(env.selection);
          break;
        case "set_persona_ack":
          if (personaRequest && env.request_id === personaRequest.id) {
            if (selectionRevision < 0) setSelection({ id: env.id, display_name: env.display_name });
            finishPersonaRequest();
          }
          break;
        case "user_input":
          log((env.speaker || "You") + ": " + (env.text || ""), "you");
          break;
        case "response_delta":
          streamReply(env);
          setStatus("Responding…", true);
          break;
        case "response_ready":
          finishReply(env);
          setStatus("Connected", true);
          break;
        case "thinking_started":
          setStatus("Thinking…", true);
          break;
        case "tool_call_started":
          if (activityInCurrentSession(env)) addActivity("Running " + (env.name || "tool") + (env.summary ? ": " + env.summary : ""));
          break;
        case "tool_call_finished":
          if (activityInCurrentSession(env)) addActivity((env.name || "Tool") + " · " + (env.status || "finished") + (env.preview ? ": " + env.preview : "") + (env.message ? " · " + env.message : ""));
          break;
        case "work_state_changed":
          showWork(env.work);
          break;
        case "audio_output_ack":
          break;
        case "audio_chunk":
          playPCM(env.data, env.sample_rate, env.segment_id, env.response_id || (env.response_envelope && env.response_envelope.response_id), env.generation);
          break;
        case "audio_end":
          if (!acceptAudioIdentity(env.generation)) break;
          if (env.reason === "interrupted" || env.reason === "error") {
            stopSegment(segmentKey(env.response_id || (env.response_envelope && env.response_envelope.response_id), env.segment_id), true);
          }
          break;
        case "audio_reset":
          applyAudioReset();
          break;
        case "turn_interrupted":
          log("interrupted", "sys");
          stopAudio(false);
          break;
        case "conversation_cleared":
          logEl.textContent = "";
          pendingReplies.clear();
          completedReplies.clear();
          clearActivity();
          setStatus("Connected", true);
          break;
        case "error":
          if (personaRequest && env.request_id === personaRequest.id) finishPersonaRequest();
          log("error: " + (env.message || JSON.stringify(env)), "err");
          break;
        default:
          break;
      }
    };
  }

  pairBtn.addEventListener("click", () => {
    const code = pairCodeEl.value.trim();
    if (!code) {
      log("enter the 6-digit pairing code from the Mac", "err");
      return;
    }
    setStatus("pairing…");
    fetch("/v1/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: code }),
    })
      .then(async (resp) => {
        const body = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          throw new Error(body.error || "pair failed (" + resp.status + ")");
        }
        if (!body.token) {
          throw new Error("pair response missing token");
        }
        tokenEl.value = body.token;
        localStorage.setItem(TOKEN_KEY, body.token);
        pairCodeEl.value = "";
        log("paired — token saved on this device", "sys");
        setStatus("paired (token ready)");
      })
      .catch((err) => {
        log("pair failed: " + err.message, "err");
        setStatus("pair failed");
      });
  });

  startBtn.addEventListener("click", () => {
    Promise.resolve().then(ensureAudio)
      .then(connect)
      .catch((err) => {
        connect();
        log("Audio unavailable: " + err.message + ". Text is still available.", "err");
      });
  });

  function bindTalk(el) {
    const down = (e) => {
      e.preventDefault();
      startTalk().catch((err) => log("mic failed: " + err, "err"));
    };
    const up = (e) => {
      e.preventDefault();
      stopTalk(false);
    };
    el.addEventListener("mousedown", down);
    el.addEventListener("mouseup", up);
    el.addEventListener("mouseleave", up);
    el.addEventListener("touchstart", down, { passive: false });
    el.addEventListener("touchend", up);
    el.addEventListener("touchcancel", up);
    el.addEventListener("keydown", (e) => {
      if ((e.key === " " || e.key === "Enter") && !e.repeat) down(e);
    });
    el.addEventListener("keyup", (e) => {
      if (e.key === " " || e.key === "Enter") up(e);
    });
  }
  bindTalk(talkBtn);

  sendBtn.addEventListener("click", () => {
    const text = inputEl.value.trim();
    if (!text) return;
    sendJSON({ type: "text_input", text: text });
    inputEl.value = "";
  });

  inputEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      sendBtn.click();
    }
  });

  if (personaEl) personaEl.addEventListener("change", () => {
    if (!connected || personaRequest || !personaEl.value || personaEl.value === selectedID) return;
    const id = "browser-persona-" + (++personaRequestSerial) + "-" + Date.now();
    personaRequest = { id: id, timer: setTimeout(() => {
      if (!personaRequest || personaRequest.id !== id) return;
      finishPersonaRequest();
      log("Persona change was not confirmed; reconnect to check the current selection.", "err");
    }, 10000) };
    personaEl.disabled = true;
    sendJSON({ type: "set_persona", name: personaEl.value, request_id: id });
  });

  if (sessionsPanel) sessionsPanel.addEventListener("toggle", () => {
    if (sessionsPanel.open) loadSessions();
  });
  if (sessionsEl && resumeBtn) {
    sessionsEl.addEventListener("change", () => { resumeBtn.disabled = !connected || !sessionsEl.value; });
    resumeBtn.addEventListener("click", resumeSession);
  }

  interruptBtn.addEventListener("click", () => {
    stopTalk(false);
    stopAudio(true);
    sendJSON({ type: "interrupt" });
  });

  clearBtn.addEventListener("click", () => {
    sendJSON({ type: "clear_history" });
  });
})();
