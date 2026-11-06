/* Persistent host settings are separate from conversation access. */
(function () {
  const $ = (id) => document.getElementById(id);
  const panel = $("managePanel");
  const status = $("manageStatus");
  const content = $("manageContent");
  const personaSelect = $("managePersona");
  let token = "";

  async function request(path, options) {
    const headers = { Authorization: "Bearer " + token };
    if (options && options.body) headers["Content-Type"] = "application/json";
    const response = await fetch(path, Object.assign({}, options || {}, { headers: headers }));
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      if (response.status === 401 || response.status === 403) content.hidden = true;
      throw new Error(data.error || "Request failed (" + response.status + ")");
    }
    return data;
  }

  function message(text) { status.textContent = text; }

  function inputFor(spec, value) {
    let input;
    if (spec.type === "bool" || spec.type === "enum") {
      input = document.createElement("select");
      const values = spec.type === "bool" ? ["true", "false"] : (spec.allows_empty ? [""].concat(spec.enum || []) : (spec.enum || []));
      const current = String(value == null ? "" : value);
      if (!values.includes(current)) values.push(current);
      for (const item of values) {
        const option = document.createElement("option");
        option.value = item;
        option.textContent = item || "App default";
        input.appendChild(option);
      }
      input.value = current;
    } else {
      input = document.createElement("input");
      input.type = "text";
      input.value = Array.isArray(value) ? JSON.stringify(value) : String(value == null ? "" : value);
    }
    return input;
  }

  function makeEntry(container, title, help, input, save) {
    const row = document.createElement("div");
    row.className = "manage-entry";
    const label = document.createElement("label");
    label.textContent = title;
    label.appendChild(input);
    const hint = document.createElement("p");
    hint.className = "hint";
    hint.textContent = help;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "secondary";
    button.textContent = "Save " + title;
    button.addEventListener("click", async () => {
      button.disabled = true;
      try { await save(input.value); } catch (err) { message(err.message); }
      finally { button.disabled = false; }
    });
    row.append(label, hint, button);
    container.appendChild(row);
    return row;
  }

  function filterSettings() {
    const query = $("manageSearch").value.trim().toLowerCase();
    for (const group of $("manageSettings").children) {
      let visible = 0;
      for (const row of group.querySelectorAll(".manage-entry")) {
        row.hidden = !!query && !row.dataset.search.includes(query);
        if (!row.hidden) visible += 1;
      }
      group.hidden = visible === 0;
      if (query && visible) group.open = true;
    }
  }

  async function loadSettings() {
    const settings = await request("/v1/manage/settings");
    const container = $("manageSettings");
    const openGroups = new Set(Array.from(container.children).filter((group) => group.open).map((group) => group.dataset.group));
    container.textContent = "";
    let group = "";
    let section;
    for (const item of settings) {
      const spec = item.spec;
      if (group !== spec.group) {
        group = spec.group;
        section = document.createElement("details");
        section.dataset.group = group;
        section.open = openGroups.has(group);
        const heading = document.createElement("summary");
        heading.textContent = group;
        section.appendChild(heading);
        container.appendChild(section);
      }
      const input = inputFor(spec, item.value.value);
      let help = spec.help + " Source: " + item.value.source + ".";
      if (item.value.source === "env") help += " An environment variable currently overrides the saved value.";
      if (spec.restart_required) help += " Restart required.";
      if (spec.type === "list<string>") help += " Enter a JSON array of strings.";
      const row = makeEntry(section, spec.title, help, input, async (value) => {
        const result = await request("/v1/manage/settings", { method: "PATCH", body: JSON.stringify({ field: spec.key, value: value }) });
        await loadSettings();
        message(result.changed ? "Saved " + spec.title + ". " + (result.restart_required ? "Restart required." : "A new conversation or restart may be needed.") : "No change to " + spec.title + ".");
      });
      row.dataset.search = (spec.key + " " + spec.title + " " + spec.help).toLowerCase();
    }
    filterSettings();
  }

  async function loadPersona() {
    const container = $("managePersonaFields");
    container.textContent = "";
    if (!personaSelect.value) return;
    const id = personaSelect.value;
    const persona = await request("/v1/manage/personas/" + encodeURIComponent(id));
    if (personaSelect.value !== id) return;
    const fields = [
      ["display_name", "Display name"], ["brain_provider", "Brain provider"], ["brain_model", "Brain model"],
      ["tts_provider", "Speech provider"], ["voice", "Voice"], ["tier", "Speech model tier"]
    ];
    if (persona.prompt_editable) fields.push(["prompt", "Persona prompt"]);
    for (const field of fields) {
      const input = field[0] === "prompt" ? document.createElement("textarea") : document.createElement("input");
      if (field[0] === "prompt") input.rows = 6;
      else input.type = "text";
      input.value = persona[field[0]] || "";
      const help = field[0] === "prompt" ? "Applies to the next turn." : field[0] === "display_name" ? "The name cannot be empty." : "An empty value inherits the app default. Stack changes apply to the next conversation.";
      makeEntry(container, field[1], help, input, async (value) => {
        await request("/v1/manage/personas/" + encodeURIComponent(id), { method: "PATCH", body: JSON.stringify({ field: field[0], value: value }) });
        await loadPersona();
        message("Saved " + field[1] + ". " + (field[0] === "prompt" ? "Applies to the next turn." : "May require a new conversation."));
      });
    }
    if (!persona.prompt_editable) {
      const note = document.createElement("p");
      note.className = "hint";
      note.textContent = "This persona's structured or built-in prompt can be edited on the host.";
      container.appendChild(note);
    }
  }

  async function loadPersonas() {
    const result = await request("/v1/personas");
    const previous = personaSelect.value;
    personaSelect.textContent = "";
    for (const persona of result.personas || []) {
      const option = document.createElement("option");
      option.value = persona.id;
      option.textContent = persona.display_name || persona.id;
      personaSelect.appendChild(option);
    }
    if (Array.from(personaSelect.options).some((option) => option.value === previous)) personaSelect.value = previous;
    await loadPersona();
  }

  async function refresh() {
    content.hidden = true;
    token = $("token").value.trim();
    if (!token) { message("Pair this device or enter a token first."); return; }
    try {
      const access = await request("/v1/config-access");
      if (!access.device_id) {
        message("This is a shared token. Pair again with a new code to get a device ID, then ask the host to grant access.");
        return;
      }
      if (!access.config_allowed) {
        message("Device ID: " + access.device_id + "\nOn the host, run: obey-voice serve devices grant " + access.device_id + "\nThen check access again.");
        return;
      }
      message("Device " + access.device_id + " can edit host settings and personas. Remote tool access is controlled separately on the host.");
      await Promise.all([loadSettings(), loadPersonas()]);
      content.hidden = false;
    } catch (err) { message(err.message); }
  }

  $("manageRefresh").addEventListener("click", refresh);
  $("manageSearch").addEventListener("input", filterSettings);
  personaSelect.addEventListener("change", () => { loadPersona().catch((err) => message(err.message)); });
  panel.addEventListener("toggle", () => { if (panel.open) refresh(); });
})();
