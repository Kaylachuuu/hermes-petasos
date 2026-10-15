(function () {
  "use strict";
  // Petasos settings: the agent's name and the Car Thing's preset buttons (its soft keys): the
  // four top buttons, and a second set of four they show after a press of the top-right button.
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
    ["music_toggle", "Play or pause music"],
    ["music_next", "Next song"],
    ["music_previous", "Previous song"],
    ["music_stop", "Stop the music"],
    ["page_petasos", "Open the Petasos page"],
    ["page_soundcloud", "Open the SoundCloud page"],
    ["page_navidrome", "Open the Navidrome page"],
    ["page_spotify", "Open the Spotify page"],
    ["page_apple", "Open the Apple Music page"],
    ["none", "Nothing"],
  ];
  // The two sets of soft keys: preset-1..4 on the top buttons, and preset-5..8 on the same
  // buttons after a press of the top-right one.
  const SETS = [
    ["First set", 0],
    ["Second set — shown after a press of the top-right button", 4],
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
    if (ACTION_LABELS[p.action]) return ACTION_LABELS[p.action];
    const words = p.action.replace(/_/g, " ");
    return words.charAt(0).toUpperCase() + words.slice(1);
  }

  // The screen's own names for these (petasos core._ACTION_LABELS).
  const ACTION_LABELS = { mic_toggle: "Mic on / off", music_next: "Next song", music_previous: "Previous song", music_toggle: "Play / pause", music_stop: "Stop the music", music_page: "Music page", soft_keys: "Other soft keys",
    page_petasos: "Petasos", page_soundcloud: "SoundCloud", page_navidrome: "Navidrome", page_spotify: "Spotify", page_apple: "Apple Music" };

  // The actions to pick from, with one an older mapping still gives a preset (such as the
  // music page) kept at the end so it shows as it is.
  function actionsFor(action) {
    if (ACTIONS.some(([a]) => a === action)) return ACTIONS;
    return ACTIONS.slice(0, -1).concat([[action, ACTION_LABELS[action]], ACTIONS[ACTIONS.length - 1]]);
  }

  // Which devices have preset buttons of their own, for the note at the top.
  function ownedBy(mapping) {
    const names = Object.entries(mapping.devices || {}).map(([id, d]) => (d && d.name) || id);
    return names.length ? " (now: " + names.join(", ") + ")" : "";
  }

  function presetsOf(mapping) {
    const out = [];
    for (let i = 1; i <= 8; i++) {
      const gestures = (mapping.controls || {})["preset-" + i] || {};
      const step = gestures.press || {};
      out.push({
        action: ACTIONS.some(([a]) => a === step.action) || ACTION_LABELS[step.action] ? step.action : "none",
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
            actionsFor(preset.action).map(([value, name]) => h(C.SelectOption, { key: value, value }, name)))),
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

  // Keeps the browser and password managers from filling these with a saved sign-in, such as Hermes's.
  const NO_AUTOFILL = { autoComplete: "off", "data-1p-ignore": true, "data-lpignore": "true", spellCheck: false };

  // Music Athena can play: a Navidrome server and folders on this machine (music.json).
  function MusicCard() {
    const [loaded, setLoaded] = useState(null);
    const [url, setUrl] = useState("");
    const [username, setUsername] = useState("");
    const [password, setPassword] = useState("");
    const [folders, setFolders] = useState("");
    const [scId, setScId] = useState("");
    const [scSecret, setScSecret] = useState("");
    const [busy, setBusy] = useState("");
    const [error, setError] = useState("");
    const [checked, setChecked] = useState(null);

    function take(data) {
      setLoaded(data);
      setUrl(data.navidrome.url);
      setUsername(data.navidrome.username);
      setPassword("");
      setFolders(data.folders.join("\n"));
      setScId((data.soundcloud || {}).client_id || "");
      setScSecret("");
    }

    useEffect(() => {
      SDK.fetchJSON(API + "/music/settings").then(take).catch((e) => setError(String(e.message || e)));
    }, []);

    async function saveAndCheck() {
      setBusy("Saving…");
      setError("");
      setChecked(null);
      try {
        const res = await SDK.authedFetch(API + "/music/settings", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            navidrome: { url: url.trim(), username: username.trim(), password },
            folders: folders.split("\n").map((f) => f.trim()).filter(Boolean),
            soundcloud: { client_id: scId.trim(), client_secret: scSecret },
          }),
        });
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.detail || "Saving failed (HTTP " + res.status + ").");
        take(body);
        setBusy("Checking the music…");
        setChecked(await SDK.fetchJSON(API + "/music/check", { method: "POST" }));
      } catch (e) {
        setError(String(e.message || e));
      } finally {
        setBusy("");
      }
    }

    if (!loaded) return error ? h("p", { className: "petasos-error" }, error) : null;
    const edited = (set) => (e) => { setChecked(null); set(e.target.value); };
    return h(C.Card, null,
      h(C.CardHeader, null, h(C.CardTitle, null, "Music")),
      h(C.CardContent, { className: "petasos-fields" },
        h("p", { className: "petasos-hint" },
          "Music the agent can play on a Petasos device when asked (“play Rumours”)."),
        h("h3", { className: "petasos-sub" }, "Navidrome"),
        h("p", { className: "petasos-hint" }, "Your Navidrome server and your Navidrome account (not your Hermes sign-in). Leave the address empty to not use it."),
        h(Field, { id: "petasos-navidrome", label: "Address", hint: "As this machine reaches it, like http://localhost:4533." },
          h(C.Input, Object.assign({ id: "petasos-navidrome", name: "navidrome-url", value: url, placeholder: "http://localhost:4533", onChange: edited(setUrl) }, NO_AUTOFILL))),
        h("div", { className: "petasos-pair" },
          h(Field, { id: "petasos-nd-user", label: "Username" },
            h(C.Input, Object.assign({ id: "petasos-nd-user", name: "navidrome-user", value: username, onChange: edited(setUsername) }, NO_AUTOFILL))),
          h(Field, { id: "petasos-nd-password", label: "Password",
            hint: loaded.navidrome.has_password ? "Saved. Type a new one to change it." : null },
            h(C.Input, Object.assign({}, NO_AUTOFILL, { id: "petasos-nd-password", name: "navidrome-password", type: "password", value: password, autoComplete: "new-password",
              placeholder: loaded.navidrome.has_password ? "••••••••" : "", onChange: edited(setPassword) })))),
        h("h3", { className: "petasos-sub" }, "Folders"),
        h(Field, { id: "petasos-folders", label: "Music folders",
          hint: "One per line, as full paths on this machine. A network share works once it is mounted here (in /etc/fstab, so it comes back after a restart)." +
            (loaded.ffmpeg ? "" : " Only MP3 files play until ffmpeg is installed.") },
          h("textarea", { id: "petasos-folders", className: "petasos-textarea", rows: 3, value: folders,
            placeholder: "/srv/music\n/mnt/nas/music", spellCheck: false, onChange: edited(setFolders) })),
        h("h3", { className: "petasos-sub" }, "SoundCloud"),
        h("p", { className: "petasos-hint" }, "Public SoundCloud tracks, through an app you register at soundcloud.com/you/apps. Played after your own music; needs ffmpeg. Leave the client ID empty to not use it."),
        h("div", { className: "petasos-pair" },
          h(Field, { id: "petasos-sc-id", label: "Client ID" },
            h(C.Input, Object.assign({ id: "petasos-sc-id", name: "soundcloud-id", value: scId, onChange: edited(setScId) }, NO_AUTOFILL))),
          h(Field, { id: "petasos-sc-secret", label: "Client secret",
            hint: (loaded.soundcloud || {}).has_secret ? "Saved. Type a new one to change it." : null },
            h(C.Input, Object.assign({}, NO_AUTOFILL, { id: "petasos-sc-secret", name: "soundcloud-secret", type: "password", value: scSecret, autoComplete: "new-password",
              placeholder: (loaded.soundcloud || {}).has_secret ? "••••••••" : "", onChange: edited(setScSecret) })))),
        h("div", { className: "petasos-actions" },
          h(C.Button, { onClick: saveAndCheck, disabled: !!busy }, busy || "Save and check"),
          error ? h("p", { className: "petasos-error" }, error) : null),
        checked
          ? h("ul", { className: "petasos-checks" },
              checked.navidrome
                ? h("li", { className: checked.navidrome.ok ? "" : "petasos-error" },
                    checked.navidrome.ok ? "Navidrome answers." : checked.navidrome.problem)
                : null,
              checked.soundcloud
                ? h("li", { className: checked.soundcloud.ok && checked.soundcloud.ffmpeg ? "" : "petasos-error" },
                    checked.soundcloud.ok
                      ? (checked.soundcloud.ffmpeg ? "SoundCloud signs the app in." : "SoundCloud signs the app in, but its songs need ffmpeg installed.")
                      : checked.soundcloud.problem)
                : null,
              checked.folders.map((f) => h("li", { key: f.path, className: f.ok ? "" : "petasos-error" },
                f.path + ": " + (f.ok ? f.songs + (f.songs === 1 ? " song" : " songs") : f.problem))))
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
      SETS.map(([title, first]) => h("section", { key: first, className: "petasos-set" },
        h("h2", { className: "petasos-set-title" }, title),
        h("div", { className: "petasos-screen", "aria-label": "How the top of the screen will look" },
          presets.slice(first, first + 4).map((p, i) => {
            const label = shownLabel(p);
            return h("span", { key: i, className: label ? "" : "off" }, label || "—");
          })),
        h("div", { className: "petasos-presets" },
          presets.slice(first, first + 4).map((p, i) =>
            h(PresetCard, { key: first + i, index: first + i, preset: p, onChange: edit(first + i) }))))),
      h("div", { className: "petasos-actions" },
        h(C.Button, { onClick: save, disabled: saving || !agentOk }, saving ? "Saving…" : "Save"),
        saved ? h("span", { className: "petasos-hint" }, "Saved.") : null,
        error ? h("p", { className: "petasos-error" }, error) : null),
      h(MusicCard, null));
  }

  window.__HERMES_PLUGINS__.register("petasos", PetasosPage);
})();
