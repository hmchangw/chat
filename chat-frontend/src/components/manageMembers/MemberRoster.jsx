import { useCallback, useEffect, useState } from 'react'
import { useNats } from '../../context/NatsContext'
import { memberList, memberRemove, memberRoleUpdate } from '../../lib/subjects'
import { ROLE_OWNER, ROLE_MEMBER } from '../../lib/constants'
import { formatAsyncJobError } from '../../lib/asyncJob'

export default function MemberRoster({ room }) {
  const { user, request, requestWithAsyncResult } = useNats()
  const [members, setMembers] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [busyKey, setBusyKey] = useState(null)
  const [actionError, setActionError] = useState(null)
  const [removeAccountInput, setRemoveAccountInput] = useState('')
  const [removeOrgInput, setRemoveOrgInput] = useState('')

  // Depend on the primitive identity fields, not the room object reference —
  // a parent re-render that hands us an equivalent-but-new room object
  // shouldn't fire a redundant member.list.
  const fetchMembers = useCallback(async () => {
    setError(null)
    setLoading(true)
    try {
      const resp = await request(memberList(user.account, room.id, room.siteId), { enrich: true })
      setMembers(resp.members ?? [])
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [request, user.account, room.id, room.siteId])

  useEffect(() => {
    fetchMembers()
  }, [fetchMembers])

  /**
   * Run a member.{remove|role-update} action through requestWithAsyncResult,
   * surface failures as a banner, and refetch the roster on success.
   *
   * @returns {Promise<boolean>} true on success, false on failure. Callers
   *   that need to perform side effects only on success (e.g. clearing an
   *   input) MUST branch on the return value — rejections are caught here
   *   and surfaced via actionError, NOT re-thrown.
   */
  const runAction = useCallback(
    async (key, subject, payload) => {
      setBusyKey(key)
      setActionError(null)
      try {
        await requestWithAsyncResult(subject, payload)
        await fetchMembers()
        return true
      } catch (err) {
        setActionError(formatAsyncJobError(err))
        return false
      } finally {
        setBusyKey(null)
      }
    },
    [requestWithAsyncResult, fetchMembers]
  )

  if (loading) return <div className="roster-loading">Loading members…</div>
  if (error) return <div className="dialog-error">{error}</div>

  const individuals = members.filter((m) => m.member?.type === 'individual')
  const orgs = members.filter((m) => m.member?.type === 'org')

  return (
    <div className="member-roster">
      {actionError && <div className="dialog-error">{actionError}</div>}

      <section>
        <h3>Members</h3>
        {individuals.length === 0 && <div className="roster-empty">No individual members.</div>}
        <ul className="roster-list">
          {individuals.map((m) => {
            const entry = m.member
            const display = entry.engName || entry.chineseName || entry.account
            const isOwner = !!entry.isOwner
            const promoteKey = `promote:${entry.account}`
            const demoteKey = `demote:${entry.account}`
            const removeKey = `remove:${entry.account}`
            return (
              <li key={m.id} className="roster-row">
                <span className="roster-name">{display}</span>
                <span className="roster-account">{entry.account}</span>
                {isOwner && <span className="roster-badge">owner</span>}
                <span className="roster-actions">
                  {isOwner ? (
                    <button
                      type="button"
                      aria-label={`Demote ${entry.account}`}
                      disabled={busyKey === demoteKey}
                      onClick={() =>
                        runAction(
                          demoteKey,
                          memberRoleUpdate(user.account, room.id, room.siteId),
                          { roomId: room.id, account: entry.account, newRole: ROLE_MEMBER }
                        )
                      }
                    >
                      Demote
                    </button>
                  ) : (
                    <button
                      type="button"
                      aria-label={`Promote ${entry.account}`}
                      disabled={busyKey === promoteKey}
                      onClick={() =>
                        runAction(
                          promoteKey,
                          memberRoleUpdate(user.account, room.id, room.siteId),
                          { roomId: room.id, account: entry.account, newRole: ROLE_OWNER }
                        )
                      }
                    >
                      Promote
                    </button>
                  )}
                  <button
                    type="button"
                    aria-label={`Remove ${entry.account}`}
                    disabled={busyKey === removeKey}
                    onClick={() =>
                      runAction(
                        removeKey,
                        memberRemove(user.account, room.id, room.siteId),
                        { roomId: room.id, account: entry.account }
                      )
                    }
                  >
                    Remove
                  </button>
                </span>
              </li>
            )
          })}
        </ul>
        <RemoveByIdRow
          id="roster-remove-account"
          label="Remove individual by account"
          buttonLabel="Remove individual"
          value={removeAccountInput}
          onChange={setRemoveAccountInput}
          busy={busyKey === 'removeById:account'}
          onSubmit={async (value) => {
            const ok = await runAction(
              'removeById:account',
              memberRemove(user.account, room.id, room.siteId),
              { roomId: room.id, account: value }
            )
            if (ok) setRemoveAccountInput('')
          }}
        />
      </section>

      <section>
        <h3>Orgs</h3>
        {orgs.length === 0 && <div className="roster-empty">No org members.</div>}
        <ul className="roster-list">
          {orgs.map((m) => {
            const entry = m.member
            const orgId = entry.id
            const display = entry.sectName || orgId
            const removeKey = `removeOrg:${orgId}`
            return (
              <li key={m.id} className="roster-row">
                <span className="roster-name">{display}</span>
                <span className="roster-account">{orgId}</span>
                {typeof entry.memberCount === 'number' && (
                  <span className="roster-count">{entry.memberCount} members</span>
                )}
                <span className="roster-actions">
                  <button
                    type="button"
                    aria-label={`Remove ${orgId}`}
                    disabled={busyKey === removeKey}
                    onClick={() =>
                      runAction(
                        removeKey,
                        memberRemove(user.account, room.id, room.siteId),
                        { roomId: room.id, orgId }
                      )
                    }
                  >
                    Remove
                  </button>
                </span>
              </li>
            )
          })}
        </ul>
        <RemoveByIdRow
          id="roster-remove-org"
          label="Remove org by id"
          buttonLabel="Remove org"
          value={removeOrgInput}
          onChange={setRemoveOrgInput}
          busy={busyKey === 'removeById:org'}
          onSubmit={async (value) => {
            const ok = await runAction(
              'removeById:org',
              memberRemove(user.account, room.id, room.siteId),
              { roomId: room.id, orgId: value }
            )
            if (ok) setRemoveOrgInput('')
          }}
        />
      </section>
    </div>
  )
}

// Escape hatch for accounts/orgs that aren't in the enriched roster — e.g.
// stale enrichment, paginated/very-large rooms, or records the user knows
// exist but can't see locally. Server enforces auth + membership; we just
// surface the typed identifier.
function RemoveByIdRow({ id, label, buttonLabel, value, onChange, busy, onSubmit }) {
  const trimmed = value.trim()
  return (
    <div className="roster-remove-by-id">
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={busy}
        placeholder="account or org id"
      />
      <button
        type="button"
        disabled={busy || !trimmed}
        onClick={() => onSubmit(trimmed)}
      >
        {buttonLabel}
      </button>
    </div>
  )
}
