import { useState, useCallback } from 'react'
import { useNats } from '../context/NatsContext'
import { useDebouncedSearch } from '../lib/useDebouncedSearch'
import { searchRooms } from '../lib/subjects'

// Module-scope handlers for the picker fields: stable references so
// EntityField props don't churn on every parent render. Only the channel
// `parseFreeText` stays inline (below) because it captures `user.siteId`.
const trimToString = (text) => text.trim() || null
const identity = (e) => e

const renderChannelResult = (r) => (
  <div className="picker-result-line">
    <strong>{r.roomName}</strong>
    <span className="picker-result-sub"> — {r.siteId}</span>
  </div>
)
const channelRefFromResult = (r) => ({ roomId: r.roomId, siteId: r.siteId })
const channelEntryKey = (c) => `${c.siteId}/${c.roomId}`
const channelEntryLabel = (c) => `${c.roomId} (${c.siteId})`
const channelResultKey = (r) => `${r.siteId}/${r.roomId}`

// Collects three entity lists for the room-service create + add-members
// payloads: users (string[]), orgs (string[]), channels (ChannelRef[]).
// Each field is a chip input — typeahead via search.rooms is wired for
// channels; users + orgs are free-text-only until the server lands the
// corresponding search endpoints.
export default function MemberPicker({
  users,
  orgs,
  channels,
  onUsersChange,
  onOrgsChange,
  onChannelsChange,
  disabled,
}) {
  const { user, request } = useNats()

  const channelFetcher = useCallback(
    async (q) => {
      const resp = await request(searchRooms(user.account), { searchText: q, scope: 'all', size: 8 })
      return resp.results ?? []
    },
    [request, user.account]
  )

  return (
    <div className="member-picker">
      <EntityField
        id="picker-users"
        label="Users"
        placeholder="Type account + Enter"
        entries={users}
        onChange={onUsersChange}
        parseFreeText={trimToString}
        entryKey={identity}
        entryLabel={identity}
        disabled={disabled}
      />
      <EntityField
        id="picker-orgs"
        label="Orgs"
        placeholder="Type org id + Enter"
        entries={orgs}
        onChange={onOrgsChange}
        parseFreeText={trimToString}
        entryKey={identity}
        entryLabel={identity}
        disabled={disabled}
      />
      <EntityField
        id="picker-channels"
        label="Channels"
        placeholder="Search a channel — or type a local-site room id + Enter"
        entries={channels}
        onChange={onChannelsChange}
        // Free-text commit assumes the typed roomId is local to the user's
        // site; cross-site picks must come from the search dropdown so the
        // siteId is sourced from server-known room metadata, not guessed.
        parseFreeText={(text) => {
          const id = text.trim()
          return id ? { roomId: id, siteId: user.siteId } : null
        }}
        entryKey={channelEntryKey}
        entryLabel={channelEntryLabel}
        searchFetcher={channelFetcher}
        renderResult={renderChannelResult}
        entryFromResult={channelRefFromResult}
        resultKey={channelResultKey}
        disabled={disabled}
      />
    </div>
  )
}

function EntityField({
  id,
  label,
  placeholder,
  entries,
  onChange,
  parseFreeText,
  entryKey,
  entryLabel,
  searchFetcher,
  renderResult,
  entryFromResult,
  resultKey,
  disabled,
}) {
  const [activeIdx, setActiveIdx] = useState(0)

  const { query, results, onChange: setQuery, reset } = useDebouncedSearch({
    delay: 250,
    minLen: 2,
    fetcher: searchFetcher,
  })
  const hasDropdown = !!searchFetcher && query.length >= 2 && results.length > 0

  const commit = (entry) => {
    if (entry == null) return
    const key = entryKey(entry)
    if (entries.some((e) => entryKey(e) === key)) return
    onChange([...entries, entry])
    reset()
    setActiveIdx(0)
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      if (hasDropdown && results[activeIdx]) {
        commit(entryFromResult(results[activeIdx]))
      } else if (query.trim()) {
        commit(parseFreeText(query))
      }
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIdx((i) => Math.min(i + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIdx((i) => Math.max(i - 1, 0))
    } else if (e.key === 'Escape') {
      reset()
      setActiveIdx(0)
    }
  }

  return (
    <div className="member-picker-field">
      <label htmlFor={id}>{label}</label>
      {entries.length > 0 && (
        <div className="member-picker-chips">
          {entries.map((entry, idx) => (
            <span key={entryKey(entry)} className="member-picker-chip">
              {entryLabel(entry)}
              <button
                type="button"
                aria-label={`Remove ${entryLabel(entry)}`}
                className="member-picker-chip-remove"
                onClick={() => onChange(entries.filter((_, i) => i !== idx))}
                disabled={disabled}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <input
        id={id}
        type="text"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value)
          setActiveIdx(0)
        }}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        disabled={disabled}
      />
      {hasDropdown && (
        <div className="member-picker-dropdown" role="listbox">
          {results.map((r, idx) => (
            <div
              key={resultKey(r)}
              role="option"
              aria-selected={idx === activeIdx}
              className={`member-picker-result${idx === activeIdx ? ' active' : ''}`}
              onClick={() => commit(entryFromResult(r))}
            >
              {renderResult(r)}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
