(function () {
  "use strict";
  // Petasos settings: the agent's name and the Car Thing's four preset buttons (its soft keys).
  // Reads and writes the button mapping through the plugin's own /mapping route, so the
  // checks and the file are the same ones the device and mapping.json use. Only each preset's
  // press is edited here; anything else in mapping.json is kept as it is.
  const SDK = window.__HERMES_PLUGIN_SDK__;
  if (!SDK || !window.__HERMES_PLUGINS__) return;

  const React = SDK.React;
  const { useState, useEffect } = SDK.hooks;
  const C = SDK.components;
  const h = React.createElement;
  const API = "/api/plugins/petasos";

  const LABEL_MAX = 32;
  const AGENT_NAME_MAX = 24;
  const ACTIONS = [
    ["prompt", "Ask a question"],
    ["conversation", "Start a conversation"],
    ["stop_speaking", "Stop speaking"],
    ["mic_toggle", "Turn the mic on or off"],
    ["none", "Nothing"],
  ];

  // What the screen shows for a preset when it has no label (petasos core.preset_labels).
  function shownLabel(p) {
    if (p.action === "none") return "";
    if (p.label.trim()) return p.label.trim();
    if (p.action === "prompt") {
      let label = p.text.trim().replace(/[?.!]+$/, "");
      if (label.length > LABEL_MAX) label = label.slice(0, LABEL_MAX - 1).replace(/\s+\S*$/, "") + "…";
      return label;
    }
    const words = p.action.replace(/_/g, " ");
    return words.charAt(0).toUpperCase() + words.slice(1);
  }

  // Which devices have preset buttons of their own, for the note at the top.
  function ownedBy(mapping) {
    const names = Object.entries(mapping.devices || {}).map(([id, d]) => (d && d.name) || id);
    return names.length ? " (now: " + names.join(", ") + ")" : "";
  }

  function presetsOf(mapping) {
    const out = [];
    for (let i = 1; i <= 4; i++) {
      const gestures = (mapping.controls || {})["preset-" + i] || {};
      const step = gestures.press || {};
      out.push({
        action: ACTIONS.some(([a]) => a === step.action) ? step.action : "none",
        text: step.text || "",
        label: step.label || "",
        other: Object.keys(gestures).filter((g) => g !== "press"),
      });
    }
    return out;
  }

  function withEdits(mapping, agent, chats, presets) {
    const next = JSON.parse(JSON.stringify(mapping));
    next.agent_name = agent.trim();
    next.chats = chats;
    next.controls = next.controls || {};
    presets.forEach((p, i) => {
      const step = { action: p.action };
      if (p.action === "prompt") step.text = p.text.trim();
      if (p.action !== "none" && p.label.trim()) step.label = p.label.trim();
      next.controls["preset-" + (i + 1)] = { press: step };
    });
    return next;
  }

  function Field(props) {
    return h("div", { className: "petasos-field" },
      h(C.Label, { htmlFor: props.id }, props.label),
      props.children,
      props.hint ? h("p", { className: "petasos-hint" }, props.hint) : null);
  }

  function PresetCard(props) {
    const { index, preset, onChange } = props;
    const set = (key) => (value) => onChange(Object.assign({}, preset, { [key]: value }));
    const id = "petasos-preset-" + (index + 1);
    return h(C.Card, { className: "petasos-preset" },
      h(C.CardHeader, null, h(C.CardTitle, null, "Preset " + (index + 1))),
      h(C.CardContent, { className: "petasos-fields" },
        h(Field, { id: id + "-action", label: "When pressed" },
          h(C.Select, { id: id + "-action", value: preset.action, onValueChange: set("action") },
            ACTIONS.map(([value, name]) => h(C.SelectOption, { key: value, value }, name)))),
        preset.action === "prompt"
          ? h(Field, { id: id + "-text", label: "What to ask" },
              h(C.Input, { id: id + "-text", value: preset.text, placeholder: "What's next on my calendar?",
                onChange: (e) => set("text")(e.target.value) }))
          : null,
        preset.action !== "none"
          ? h(Field, { id: id + "-label", label: "Label on the screen",
              hint: preset.label.trim() ? null : "Leave empty to show “" + shownLabel(preset) + "”." },
              h(C.Input, { id: id + "-label", value: preset.label, maxLength: LABEL_MAX,
                placeholder: shownLabel(Object.assign({}, preset, { label: "" })),
                onChange: (e) => set("label")(e.target.value) }))
          : null,
        preset.other.length
          ? h("p", { className: "petasos-hint" },
              "mapping.json also gives this button " + preset.other.join(" and ") +
              " gestures; saving here replaces them with this press.")
          : null));
  }

  function PetasosPage() {
    const [loaded, setLoaded] = useState(null); // { mapping, problems, path }
    const [agent, setAgent] = useState("");
    const [chats, setChats] = useState("separate");
    const [presets, setPresets] = useState([]);
    const [error, setError] = useState("");
    const [saving, setSaving] = useState(false);
    const [saved, setSaved] = useState(false);

    function take(data) {
      setLoaded(data);
      setAgent(data.mapping.agent_name || "Hermes");
      setChats(data.mapping.chats === "shared" ? "shared" : "separate");
      setPresets(presetsOf(data.mapping));
    }

    useEffect(() => {
      SDK.fetchJSON(API + "/mapping").then(take).catch((e) => setError(String(e.message || e)));
    }, []);

    async function save() {
      setSaving(true);
      setError("");
      setSaved(false);
      try {
        const res = await SDK.authedFetch(API + "/mapping", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(withEdits(loaded.mapping, agent, chats, presets)),
        });
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.detail || "Saving failed (HTTP " + res.status + ").");
        take(Object.assign({}, loaded, { mapping: body.mapping, problems: [] }));
        setSaved(true);
      } catch (e) {
        setError(String(e.message || e));
      } finally {
        setSaving(false);
      }
    }

    if (!loaded) {
      return h("div", { className: "petasos-page" },
        error ? h("p", { className: "petasos-error" }, error) : h("p", { className: "petasos-hint" }, "Loading…"));
    }
    const edit = (i) => (p) => {
      setSaved(false);
      setPresets(presets.map((old, j) => (j === i ? p : old)));
    };
    const agentOk = agent.trim().length > 0 && agent.trim().length <= AGENT_NAME_MAX;
    return h("div", { className: "petasos-page" },
      h("header", { className: "petasos-head" },
        h("h1", null, "Petasos"),
        h("p", { className: "petasos-hint" },
          "Who Petasos talks to, its chats, and the shared preset buttons, which every device without its own uses. A Car Thing gets its own in its Device settings, an app in its Settings" + ownedBy(loaded.mapping) + ". Devices pick up changes the next time a button is pressed. Saved to " + loaded.path + ".")),
      loaded.problems && loaded.problems.length
        ? h("p", { className: "petasos-error" },
            "mapping.json has problems, so the Car Thing is using the last good mapping: " + loaded.problems.join(" "))
        : null,
      h(C.Card, null,
        h(C.CardHeader, null, h(C.CardTitle, null, "Agent")),
        h(C.CardContent, { className: "petasos-fields" },
          h(Field, { id: "petasos-agent", label: "Name", hint: "Shown on the screen (“Messenger of " + (agent.trim() || "…") + "”) and used in its messages." },
            h(C.Input, { id: "petasos-agent", value: agent, maxLength: AGENT_NAME_MAX,
              onChange: (e) => { setSaved(false); setAgent(e.target.value); } })),
          h(Field, { id: "petasos-chats", label: "Chats",
            hint: chats === "shared"
              ? "The Car Thing and every Mac app carry on one chat, “Petasos”."
              : "Each device has its own chat: “Petasos · Car Thing”, “Petasos · Office Mac” and so on." },
            h(C.Select, { id: "petasos-chats", value: chats, onValueChange: (v) => { setSaved(false); setChats(v); } },
              h(C.SelectOption, { value: "separate" }, "A chat for each device"),
              h(C.SelectOption, { value: "shared" }, "One chat for all devices"))))),
      h("div", { className: "petasos-screen", "aria-label": "How the top of the screen will look" },
        presets.map((p, i) => {
          const label = shownLabel(p);
          return h("span", { key: i, className: label ? "" : "off" }, label || "—");
        })),
      h("div", { className: "petasos-presets" },
        presets.map((p, i) => h(PresetCard, { key: i, index: i, preset: p, onChange: edit(i) }))),
      h("div", { className: "petasos-actions" },
        h(C.Button, { onClick: save, disabled: saving || !agentOk }, saving ? "Saving…" : "Save"),
        saved ? h("span", { className: "petasos-hint" }, "Saved.") : null,
        error ? h("p", { className: "petasos-error" }, error) : null));
  }

  window.__HERMES_PLUGINS__.register("petasos", PetasosPage);
})();
