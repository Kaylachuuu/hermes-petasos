// Petasos, the Hermes Desktop half: its settings under Settings → Plugins → Petasos.
//
// The same settings as the dashboard's Petasos tab (dashboard/dist/index.js): the agent's name,
// whether devices share one chat, and the four preset buttons (the soft keys). It reads and
// writes the plugin's /mapping route through the app's own connection to Hermes, so the checks
// and the file are the ones the Car Thing and mapping.json use. Only each preset's press is
// edited here; anything else in mapping.json is kept as it is.

import { useEffect, useState } from 'react'
import { jsx, jsxs } from 'react/jsx-runtime'

const LABEL_MAX = 32
// The screen's own names for these (petasos core._ACTION_LABELS).
const ACTION_LABELS = { mic_toggle: 'Mic on / off', music_next: 'Next song', music_previous: 'Previous song', music_toggle: 'Play / pause', music_stop: 'Stop the music' }
const AGENT_NAME_MAX = 24
const TABS = [['agent', 'Agent'], ['keys', 'Soft keys'], ['music', 'Music']]
const ACTIONS = [
  ['prompt', 'Ask a question'],
  ['conversation', 'Start a conversation'],
  ['stop_speaking', 'Stop speaking'],
  ['mic_toggle', 'Turn the mic on or off'],
  ['music_toggle', 'Play or pause music'],
  ['music_next', 'Next song'],
  ['music_previous', 'Previous song'],
  ['music_stop', 'Stop the music'],
  ['none', 'Nothing']
]

const FIELD = 'w-full rounded border border-(--ui-border) bg-transparent px-2 py-1 text-sm'
const BUTTON = 'rounded border border-(--ui-border) px-3 py-1 text-sm hover:bg-(--ui-bg-hover) disabled:opacity-50'
const HINT = 'text-(--ui-text-tertiary)'
const ERROR = 'text-(--ui-text-danger,#d9543a)'

// What the screen shows for a preset when it has no label (petasos core.preset_labels).
export function shownLabel(p) {
  if (p.action === 'none') {
    return ''
  }

  if (p.label.trim()) {
    return p.label.trim()
  }

  if (p.action === 'prompt') {
    let label = p.text.trim().replace(/[?.!]+$/, '')

    if (label.length > LABEL_MAX) {
      label = label.slice(0, LABEL_MAX - 1).replace(/\s+\S*$/, '') + '…'
    }

    return label
  }

  if (ACTION_LABELS[p.action]) {
    return ACTION_LABELS[p.action]
  }

  const words = p.action.replace(/_/g, ' ')

  return words.charAt(0).toUpperCase() + words.slice(1)
}

// Which devices have preset buttons of their own, for the note at the top.
export function ownedBy(mapping) {
  const names = Object.entries(mapping.devices || {}).map(([id, d]) => d?.name || id)

  return names.length ? ` (now: ${names.join(', ')})` : ''
}

export function presetsOf(mapping) {
  const out = []

  for (let i = 1; i <= 4; i++) {
    const gestures = (mapping.controls || {})[`preset-${i}`] || {}
    const step = gestures.press || {}

    out.push({
      action: ACTIONS.some(([a]) => a === step.action) ? step.action : 'none',
      label: step.label || '',
      other: Object.keys(gestures).filter(g => g !== 'press'),
      text: step.text || ''
    })
  }

  return out
}

export function withEdits(mapping, agent, chats, presets) {
  const next = JSON.parse(JSON.stringify(mapping))

  next.agent_name = agent.trim()
  next.chats = chats
  next.controls = next.controls || {}
  presets.forEach((p, i) => {
    const step = { action: p.action }

    if (p.action === 'prompt') {
      step.text = p.text.trim()
    }

    if (p.action !== 'none' && p.label.trim()) {
      step.label = p.label.trim()
    }

    next.controls[`preset-${i + 1}`] = { press: step }
  })

  return next
}

function labelled(title, input, hint) {
  return jsxs('label', {
    className: 'flex flex-col gap-1',
    children: [jsx('span', { className: 'font-medium', children: title }), input, hint ? jsx('span', { className: HINT, children: hint }) : null]
  })
}

function select(value, onChange, options) {
  return jsx('select', {
    className: FIELD,
    onChange: e => onChange(e.target.value),
    value,
    children: options.map(([v, name]) => jsx('option', { value: v, children: name }, v))
  })
}

function Preset({ index, preset, onChange }) {
  const set = key => value => onChange({ ...preset, [key]: value })

  return jsxs('fieldset', {
    className: 'flex flex-col gap-3 rounded border border-(--ui-border) p-3',
    children: [
      jsx('legend', { className: 'px-1 font-medium', children: `Preset ${index + 1}` }),
      labelled('When pressed', select(preset.action, set('action'), ACTIONS)),
      preset.action === 'prompt'
        ? labelled('What to ask',
            jsx('input', {
              className: FIELD,
              onChange: e => set('text')(e.target.value),
              placeholder: "What's next on my calendar?",
              value: preset.text
            }))
        : null,
      preset.action !== 'none'
        ? labelled('Label on the screen',
            jsx('input', {
              className: FIELD,
              maxLength: LABEL_MAX,
              onChange: e => set('label')(e.target.value),
              placeholder: shownLabel({ ...preset, label: '' }),
              value: preset.label
            }),
            preset.label.trim() ? null : `Leave empty to show “${shownLabel(preset)}”.`)
        : null,
      preset.other.length
        ? jsx('span', {
            className: HINT,
            children: `mapping.json also gives this button ${preset.other.join(' and ')} gestures; saving here replaces them with this press.`
          })
        : null
    ]
  })
}

// Keeps the browser and password managers from filling these with a saved sign-in, such as Hermes's.
const NO_AUTOFILL = { autoComplete: 'off', 'data-1p-ignore': true, 'data-lpignore': 'true', spellCheck: false }

function group(title, hint, children) {
  return jsxs('fieldset', {
    className: 'flex flex-col gap-3 rounded border border-(--ui-border) p-3',
    children: [
      jsx('legend', { className: 'px-1 font-medium', children: title }),
      hint ? jsx('span', { className: HINT, children: hint }) : null,
      ...children
    ]
  })
}

// Music the agent can play (the plugin's music.json on Hermes): Navidrome and folders.
function Music({ ctx }) {
  const [loaded, setLoaded] = useState(null)
  const [url, setUrl] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [folders, setFolders] = useState('')
  const [scId, setScId] = useState('')
  const [scSecret, setScSecret] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [checked, setChecked] = useState(null)

  const take = data => {
    setLoaded(data)
    setUrl(data.navidrome.url || '')
    setUsername(data.navidrome.username || '')
    setPassword('')
    setFolders((data.folders || []).join('\n'))
    setScId(data.soundcloud?.client_id || '')
    setScSecret('')
  }

  useEffect(() => {
    ctx.rest('/music/settings').then(take).catch(e => setError(`Couldn't load the music settings (the plugin on Hermes may need updating): ${String(e?.message || e)}`))
  }, [])

  const save = async () => {
    setBusy('Saving…')
    setError('')
    setChecked(null)

    try {
      take(await ctx.rest('/music/settings', {
        body: {
          folders: folders.split('\n').map(f => f.trim()).filter(Boolean),
          navidrome: { password, url: url.trim(), username: username.trim() },
          soundcloud: { client_id: scId.trim(), client_secret: scSecret }
        },
        method: 'PUT'
      }))
      setBusy('Checking the music…')
      setChecked(await ctx.rest('/music/check', { body: {}, method: 'POST' }))
    } catch (e) {
      setError(String(e?.message || e))
    } finally {
      setBusy('')
    }
  }

  const edited = fn => value => {
    setChecked(null)
    fn(value)
  }

  if (!loaded) {
    return error ? jsx('span', { className: ERROR, children: error }) : null
  }

  return jsxs('fieldset', {
    className: 'flex flex-col gap-3 rounded border border-(--ui-border) p-3',
    children: [
      jsx('legend', { className: 'px-1 font-medium', children: 'Music' }),
      jsx('span', { className: HINT, children: 'What the agent can play on a Petasos device when asked (“play Rumours”). Saved on the Hermes this app is connected to.' }),
      group('Navidrome', 'Your Navidrome server, signed in to with your Navidrome account (not your Hermes sign-in). Leave the address empty to not use it.', [
        labelled('Address',
          jsx('input', { ...NO_AUTOFILL, className: FIELD, name: 'navidrome-url', onChange: e => edited(setUrl)(e.target.value), placeholder: 'http://localhost:4533', value: url }),
          'As the Hermes machine reaches it.'),
        labelled('Navidrome username',
          jsx('input', { ...NO_AUTOFILL, className: FIELD, name: 'navidrome-user', onChange: e => edited(setUsername)(e.target.value), value: username })),
        labelled('Navidrome password',
          jsx('input', {
            ...NO_AUTOFILL,
            autoComplete: 'new-password',
            className: FIELD,
            name: 'navidrome-password',
            onChange: e => edited(setPassword)(e.target.value),
            placeholder: loaded.navidrome.has_password ? '••••••••' : '',
            type: 'password',
            value: password
          }),
          loaded.navidrome.has_password ? 'Saved. Type a new one to change it.' : null)
      ]),
      group('Folders', null, [
        jsx('textarea', {
          'aria-label': 'Music folders',
          className: `${FIELD} font-mono`,
          onChange: e => edited(setFolders)(e.target.value),
          placeholder: '/srv/music\n/mnt/nas/music',
          rows: 3,
          spellCheck: false,
          value: folders
        }),
        jsx('span', {
          className: HINT,
          children: 'One per line, as full paths on the Hermes machine. A network share works once it is mounted there.' +
            (loaded.ffmpeg ? '' : ' Only MP3 files play until ffmpeg is installed on it.')
        })
      ]),
      group('SoundCloud', 'Public SoundCloud tracks, through an app you register at soundcloud.com/you/apps. Played after your own music. Needs ffmpeg on the Hermes machine. Leave the client ID empty to not use it.', [
        labelled('Client ID',
          jsx('input', { ...NO_AUTOFILL, className: FIELD, name: 'soundcloud-id', onChange: e => edited(setScId)(e.target.value), value: scId })),
        labelled('Client secret',
          jsx('input', {
            ...NO_AUTOFILL,
            autoComplete: 'new-password',
            className: FIELD,
            name: 'soundcloud-secret',
            onChange: e => edited(setScSecret)(e.target.value),
            placeholder: loaded.soundcloud?.has_secret ? '••••••••' : '',
            type: 'password',
            value: scSecret
          }),
          loaded.soundcloud?.has_secret ? 'Saved. Type a new one to change it.' : null),
        jsx('span', {
          className: HINT,
          children: loaded.soundcloud?.account
            ? `Signed in as ${loaded.soundcloud.account}: the agent can play your likes and playlists.`
            : 'For your likes and playlists, sign in from the Petasos app on a Mac or Windows computer (Settings → Music).'
        })
      ]),
      jsxs('div', {
        className: 'flex items-center gap-3',
        children: [
          jsx('button', { className: BUTTON, disabled: !!busy, onClick: save, type: 'button', children: busy || 'Save and check' }),
          error ? jsx('span', { className: ERROR, children: error }) : null
        ]
      }),
      checked
        ? jsxs('ul', {
            className: 'flex list-disc flex-col gap-1 pl-5',
            children: [
              checked.navidrome
                ? jsx('li', { className: checked.navidrome.ok ? '' : ERROR, children: checked.navidrome.ok ? 'Navidrome answers.' : checked.navidrome.problem })
                : null,
              checked.soundcloud
                ? jsx('li', {
                    className: checked.soundcloud.ok && checked.soundcloud.ffmpeg ? '' : ERROR,
                    children: checked.soundcloud.ok
                      ? (checked.soundcloud.ffmpeg ? 'SoundCloud signs the app in.' : 'SoundCloud signs the app in, but its songs need ffmpeg installed on Hermes.')
                      : checked.soundcloud.problem
                  })
                : null,
              ...checked.folders.map(f =>
                jsx('li', { className: f.ok ? '' : ERROR, children: `${f.path}: ${f.ok ? `${f.songs} ${f.songs === 1 ? 'song' : 'songs'}` : f.problem}` }, f.path))
            ]
          })
        : null
    ]
  })
}

function Settings({ ctx }) {
  const [loaded, setLoaded] = useState(null) // { mapping, problems, path }
  const [agent, setAgent] = useState('')
  const [chats, setChats] = useState('separate')
  const [presets, setPresets] = useState([])
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [tab, setTab] = useState('agent')

  const take = data => {
    setLoaded(data)
    setAgent(data.mapping.agent_name || 'Hermes')
    setChats(data.mapping.chats === 'shared' ? 'shared' : 'separate')
    setPresets(presetsOf(data.mapping))
  }

  useEffect(() => {
    ctx.rest('/mapping').then(take).catch(e => setError(`Couldn't reach the Petasos plugin on Hermes: ${String(e?.message || e)}`))
  }, [])

  const save = async () => {
    setSaving(true)
    setError('')
    setSaved(false)

    try {
      const body = await ctx.rest('/mapping', { body: withEdits(loaded.mapping, agent, chats, presets), method: 'PUT' })

      take({ ...loaded, mapping: body.mapping, problems: [] })
      setSaved(true)
    } catch (e) {
      setError(String(e?.message || e))
    } finally {
      setSaving(false)
    }
  }

  if (!loaded) {
    return jsx('div', { className: 'p-4 text-sm', children: jsx('span', { className: error ? ERROR : HINT, children: error || 'Loading…' }) })
  }

  const edited = fn => value => {
    setSaved(false)
    fn(value)
  }

  const agentOk = agent.trim().length > 0 && agent.trim().length <= AGENT_NAME_MAX

  const saveRow = jsxs('div', {
    className: 'flex items-center gap-3',
    children: [
      jsx('button', { className: BUTTON, disabled: saving || !agentOk, onClick: save, type: 'button', children: saving ? 'Saving…' : 'Save' }),
      saved ? jsx('span', { className: HINT, children: 'Saved.' }) : null,
      error ? jsx('span', { className: ERROR, children: error }) : null
    ]
  })

  // Tabs, as on the Car Thing's settings. The agent and soft keys share one Save; Music has its own.
  const tabs = jsx('div', {
    className: 'flex gap-1 border-b border-(--ui-border)',
    role: 'tablist',
    children: TABS.map(([id, name]) =>
      jsx('button', {
        'aria-selected': tab === id,
        className: `-mb-px border-b-2 px-3 py-1.5 ${tab === id ? 'border-current font-medium' : `border-transparent ${HINT}`}`,
        onClick: () => setTab(id),
        role: 'tab',
        type: 'button',
        children: name
      }, id))
  })

  const agentTab = [
    jsx('span', {
      className: HINT,
      children: `Who Petasos talks to and its chats. Saved on Hermes to ${loaded.path}.`
    }),
    loaded.problems?.length
      ? jsx('span', { className: ERROR, children: `mapping.json has problems, so Petasos is using the last good mapping: ${loaded.problems.join(' ')}` })
      : null,
    labelled('Agent name',
      jsx('input', { className: FIELD, maxLength: AGENT_NAME_MAX, onChange: e => edited(setAgent)(e.target.value), value: agent }),
      `Shown on the screen (“Messenger of ${agent.trim() || '…'}”) and in its messages, such as “${agent.trim() || '…'} is listening”.`),
    labelled('Chats',
      select(chats, edited(setChats), [
        ['separate', 'A chat for each device'],
        ['shared', 'One chat for all devices']
      ]),
      chats === 'shared'
        ? 'The Car Thing and every Petasos app carry on one chat, “Petasos”.'
        : 'Each device has its own chat: “Petasos · Car Thing”, “Petasos · Office Mac” and so on.'),
    saveRow
  ]

  const keysTab = [
    jsx('span', {
      className: HINT,
      children: `The shared preset buttons, which every device without its own uses. A Car Thing gets its own in its Device settings, a Petasos app in its Settings${ownedBy(loaded.mapping)}. Devices pick up changes the next time a button is pressed.`
    }),
    jsx('div', {
      'aria-label': 'How the top of the screen will look',
      className: 'grid grid-cols-4 gap-2 rounded bg-[#1f1b17] p-3 text-center text-[#c9b48a]',
      children: presets.map((p, i) => jsx('span', { className: shownLabel(p) ? '' : 'opacity-40', children: shownLabel(p) || '—' }, i))
    }),
    ...presets.map((p, i) =>
      jsx(Preset, { index: i, onChange: edited(next => setPresets(presets.map((old, j) => (j === i ? next : old)))), preset: p }, i)),
    saveRow
  ]

  return jsxs('div', {
    className: 'flex max-w-xl flex-col gap-4 p-4 text-sm',
    children: [tabs, ...(tab === 'music' ? [jsx(Music, { ctx }, 'music')] : tab === 'keys' ? keysTab : agentTab)]
  })
}

export default {
  id: 'petasos',
  name: 'Petasos',
  register(ctx) {
    ctx.registerSettingsPage?.({
      icon: 'mic',
      id: 'settings',
      render: () => jsx(Settings, { ctx }),
      title: 'Petasos'
    })
  }
}
